package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 对象存储的两段式启动是 P8.5 的核心承诺（方案 15 §11.2），这四条用例就是它的四条边界：
// 配置形状非法必须拒启；外部依赖此刻不在必须降级启动；off 不是故障；db 随平台库就绪。

func blobTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Platform.Blobs = config.PlatformBlobsConfig{
		Prefix:        objectstore.DefaultPrefix,
		MaxObjectSize: "1MiB",
		PresignTTL:    time.Minute,
		GCGracePeriod: time.Hour,
	}
	return cfg
}

func blobTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite("file:cli-blobs-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func blobTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestNewBlobGatewayRefusesMalformedConfig 锁「配置写错就拒启」：这类错误永远不会自愈，
// 降级跑起来只会在每次下载时回一句 503，比启动即失败更难归因。
func TestNewBlobGatewayRefusesMalformedConfig(t *testing.T) {
	cases := []struct {
		name  string
		patch func(*config.PlatformBlobsConfig)
	}{
		{"s3 缺 endpoint", func(b *config.PlatformBlobsConfig) {
			b.Driver = config.BlobDriverS3
			b.Bucket, b.AccessKey, b.SecretKey = "k8s-ai", "ak", "sk"
		}},
		{"s3 缺 access_key", func(b *config.PlatformBlobsConfig) {
			b.Driver = config.BlobDriverS3
			b.Endpoint, b.Bucket, b.SecretKey = "minio.internal:9000", "k8s-ai", "sk"
		}},
		{"dir 缺根目录", func(b *config.PlatformBlobsConfig) {
			b.Driver = config.BlobDriverDir
			b.Dir = "  "
		}},
		{"driver 拼错", func(b *config.PlatformBlobsConfig) { b.Driver = "minio" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := blobTestConfig(t)
			tc.patch(&cfg.Platform.Blobs)
			gw, err := newBlobGateway(context.Background(), cfg, blobTestStore(t), blobTestLogger())
			if err == nil {
				t.Fatalf("配置形状非法时必须拒绝启动，got gateway driver=%s", gw.Driver())
			}
			if gw != nil {
				t.Fatalf("拒启时不应返回网关：%+v", gw)
			}
			if !errors.Is(err, objectstore.ErrKeyInvalid) {
				t.Fatalf("配置错误应归为构造期 ErrKeyInvalid：%v", err)
			}
		})
	}
}

// TestNewBlobGatewayStartsDegradedOnBackendOutage 是这一期最重要的一条：桶/根目录此刻
// 不在，平台必须照常起来（MySQL 才是核心依赖），只有对象类端点回 503。
func TestNewBlobGatewayStartsDegradedOnBackendOutage(t *testing.T) {
	t.Run("dir 根目录不可用", func(t *testing.T) {
		cfg := blobTestConfig(t)
		// 把根目录指向一个普通文件：MkdirAll 必然失败，等价于挂载点缺失/卷没挂上。
		blocker := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.Platform.Blobs.Driver = config.BlobDriverDir
		cfg.Platform.Blobs.Dir = blocker

		gw, err := newBlobGateway(context.Background(), cfg, blobTestStore(t), blobTestLogger())
		if err != nil {
			t.Fatalf("后端不可达不应阻止启动：%v", err)
		}
		if gw == nil {
			t.Fatal("降级启动也必须交出网关，否则对象端点无法回 503")
		}
		defer func() { _ = gw.Close() }()
		if gw.Available() {
			t.Fatal("根目录坏掉时不应标记为就绪")
		}
		health := gw.Health(context.Background())
		if health.Status != service.BlobStatusDegraded || health.Ready || health.ProbeError == "" {
			t.Fatalf("健康结论应是 degraded 且带原因：%+v", health)
		}
	})

	t.Run("s3 桶不可达", func(t *testing.T) {
		cfg := blobTestConfig(t)
		cfg.Platform.Blobs.Driver = config.BlobDriverS3
		// 本机已关闭的端口：连接立刻被拒，等价于桶/网络故障（真机 S3 门禁另见交付说明）。
		cfg.Platform.Blobs.Endpoint = "127.0.0.1:1"
		cfg.Platform.Blobs.Bucket = "aegisops-test"
		cfg.Platform.Blobs.AccessKey = "ak"
		cfg.Platform.Blobs.SecretKey = "sk"
		cfg.Platform.Blobs.PathStyle = true
		cfg.Platform.Blobs.CreateBucket = true

		gw, err := newBlobGateway(context.Background(), cfg, blobTestStore(t), blobTestLogger())
		if err != nil {
			t.Fatalf("桶不可达不应阻止启动：%v", err)
		}
		if gw == nil {
			t.Fatal("降级启动也必须交出网关")
		}
		defer func() { _ = gw.Close() }()
		if gw.Driver() != objectstore.DriverS3 {
			t.Fatalf("driver = %s，期望 s3", gw.Driver())
		}
		if gw.Available() {
			t.Fatal("桶连不上时不应标记为就绪")
		}
		if health := gw.Health(context.Background()); health.Status != service.BlobStatusDegraded || health.ProbeError == "" {
			t.Fatalf("健康结论应是 degraded 且带原因：%+v", health)
		}
		// 对象类调用必须拿到可翻译成 503 的哨兵错误，而不是一句普通内部错误。
		if _, err := gw.Put(context.Background(), objectstore.Spec{
			Category: objectstore.CategoryReports, Scope: []string{"c1"}, Ext: ".md",
		}, nil); !errors.Is(err, objectstore.ErrUnavailable) {
			t.Fatalf("未就绪时 Put 应回 ErrUnavailable：%v", err)
		}
	})
}

// TestNewBlobGatewayOffIsNotAnOutage 区分「没开这个功能」与「后端坏了」：off 报 disabled，
// 不报 degraded，否则运维会去查一个根本没启用的桶。
func TestNewBlobGatewayOffIsNotAnOutage(t *testing.T) {
	cfg := blobTestConfig(t)
	cfg.Platform.Blobs.Driver = config.BlobDriverOff

	gw, err := newBlobGateway(context.Background(), cfg, blobTestStore(t), blobTestLogger())
	if err != nil || gw == nil {
		t.Fatalf("driver=off 是合法配置：gw=%v err=%v", gw, err)
	}
	health := gw.Health(context.Background())
	if health.Status != service.BlobStatusDisabled || health.Ready {
		t.Fatalf("off 应报 disabled：%+v", health)
	}
	if _, err := gw.Put(context.Background(), objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"c1"}, Ext: ".md",
	}, nil); !errors.Is(err, objectstore.ErrDisabled) {
		t.Fatalf("off 时 Put 应回 ErrDisabled：%v", err)
	}
}

// TestNewBlobGatewayDBBackendIsReady 守住默认路径：driver=db 的正文随平台库就绪，
// 并且装配要真的把平台库注入进来（忘了注入就是构造期 ErrKeyInvalid）。
func TestNewBlobGatewayDBBackendIsReady(t *testing.T) {
	cfg := blobTestConfig(t)
	cfg.Platform.Blobs.Driver = config.BlobDriverDB

	gw, err := newBlobGateway(context.Background(), cfg, blobTestStore(t), blobTestLogger())
	if err != nil || gw == nil {
		t.Fatalf("driver=db 应直接就绪：gw=%v err=%v", gw, err)
	}
	defer func() { _ = gw.Close() }()
	if !gw.Available() {
		t.Fatal("平台库可用时 db 后端应就绪")
	}
	if health := gw.Health(context.Background()); health.Status != service.BlobStatusReady || health.Driver != objectstore.DriverDB {
		t.Fatalf("健康结论应是 ready/db：%+v", health)
	}
}
