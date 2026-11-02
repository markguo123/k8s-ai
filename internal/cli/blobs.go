package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// newBlobGateway 装配对象存储网关（P8.5，方案 15 §11.2 两段式启动）。
//
// 两段式的分界在这里，也只有在这里：
//
//   - **构造期错误 → 拒绝启动**。driver 拼错、s3 四件套缺项、dir 根目录形状非法，
//     都是"配置写错了"，起来也是半瘫，让人工立刻看到比降级跑一小时更负责。
//   - **就绪期错误 → 记 ERROR 后照样启动**。桶不可达、桶不存在且无建桶权限、凭据被拒、
//     dir 抢不到独占锁，都是"外部依赖此刻不在了"。MySQL 在，聊天/审批/审计/扫描/设置
//     就必须照常服务，只有文件相关端点回 503 + Retry-After。
//
// 让 S3 抖动决定整个平台是否上线，等于把一个可降级依赖升级成了核心依赖；反过来把配置
// 错误也降级，则会让一个永远不可能自愈的进程在集群里一直 Running。
func newBlobGateway(ctx context.Context, cfg *config.Config, st store.Store, logger *slog.Logger) (*service.BlobGateway, error) {
	blobs := cfg.Platform.Blobs
	maxBytes, err := blobs.MaxObjectSizeBytes()
	if err != nil {
		return nil, fmt.Errorf("platform.blobs.max_object_size: %w", err)
	}
	backend, err := objectstore.New(objectstore.Config{
		Driver:       blobs.EffectiveDriver(),
		Prefix:       blobs.Prefix,
		Dir:          blobs.Dir,
		TmpDir:       blobs.TmpDir,
		MaxBytes:     maxBytes,
		Endpoint:     blobs.Endpoint,
		Region:       blobs.Region,
		UseTLS:       blobs.UseTLS,
		PathStyle:    blobs.PathStyle,
		Bucket:       blobs.Bucket,
		AccessKey:    blobs.AccessKey,
		SecretKey:    blobs.SecretKey,
		CreateBucket: blobs.CreateBucket,
		PresignTTL:   blobs.PresignTTL,
	}, func() (objectstore.Store, error) {
		// db 后端的内容后端就在平台库里：惰性注入让 dir/s3 的部署不必为了构造后端
		// 先去开一次连接池（工厂只在 driver=db 时调用这个闭包）。
		return st.ObjectContent(blobs.Prefix, maxBytes)
	})
	if err != nil {
		return nil, fmt.Errorf("object storage backend: %w", err)
	}

	gw := service.NewBlobGateway(backend, st.Objects())

	if err := gw.EnsureReady(ctx); err != nil {
		if errors.Is(err, objectstore.ErrDisabled) {
			// driver=off 是"这个功能没开"，不是故障：报 INFO，健康端点回 disabled。
			logger.Info("object storage disabled (platform.blobs.driver=off); report archiving is off")
			return gw, nil
		}
		logger.Error("object storage degraded; object APIs will answer 503, chat/approval/audit unaffected",
			"driver", gw.Driver(), "error", err.Error())
		return gw, nil
	}

	// db 后端把正文塞进 LONGBLOB，MySQL 那一侧还有自己的上限（max_allowed_packet）。
	// 装配层只比一次、点名一次：等用户上传到一半才拿到 ErrTooLarge，运维根本猜不到
	// 是"配置写了 64MiB、库其实只吃 16MiB-1"（§13.4）。
	if gw.Driver() == objectstore.DriverDB {
		if limit := store.ObjectContentLimit(maxBytes); limit != maxBytes {
			logger.Warn("object size limit clamped by the database backend",
				"configured_bytes", maxBytes, "effective_bytes", limit,
				"hint", "raise MySQL max_allowed_packet, or switch platform.blobs.driver to dir/s3")
		}
	}

	reportDriverMismatch(logger, ctx, st, gw.Driver())
	logger.Info("object storage ready", "driver", gw.Driver())
	return gw, nil
}

// reportDriverMismatch 启动期点名「内容还在别的后端」的对象（方案 15 §10.3）。
//
// 只报警不拦启动是刻意的：改一行配置就让平台起不来，运维既看不出为什么、也没法先把
// 配置改回去；而把数量与迁移命令打进启动日志，再在 storage/health 里给出
// driver_mismatch 计数，足够让人在下一次读历史报告之前先发现问题。
func reportDriverMismatch(logger *slog.Logger, ctx context.Context, st store.Store, driver string) {
	stats, err := st.Objects().ObjectStats(ctx)
	if err != nil {
		logger.Warn("object inventory scan failed", "error", err.Error())
		return
	}
	var objects, bytes int64
	var others []string
	for _, s := range stats {
		if s.Driver == driver {
			continue
		}
		objects += s.Objects
		bytes += s.Bytes
		others = append(others, fmt.Sprintf("%s=%d", s.Driver, s.Objects))
	}
	if objects == 0 {
		return
	}
	logger.Error("objects were written by another backend and are not readable right now",
		"current_driver", driver, "objects", objects, "bytes", bytes, "by_driver", fmt.Sprint(others),
		"fix", fmt.Sprintf("k8s-ai blobs migrate --to %s", driver))
}
