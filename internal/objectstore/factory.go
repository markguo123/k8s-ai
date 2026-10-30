// 本文件是对象存储后端的唯一装配入口（方案 15 §4.5）：server 与 CLI 都经它拿到
// Store，业务模块自己不 switch driver。有了这一层，platform.blobs.driver 的取值
// 白名单只有一份实现，不会出现「server 认 s3、CLI 也照抄一遍却忘了 CLI 没有桶」。

package objectstore

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Config 是构造后端需要的形态参数，与 platform.blobs.* 一一对应。
//
// 本包不 import internal/config（抽象层不认配置文件，也不认 MySQL），所以由装配层
// 把配置结构体翻成这里：字段名保持与配置键同义，翻译处一眼能核对。
type Config struct {
	// Driver 取 off | db | dir | s3；空值按默认后端 db（与 config 的 EffectiveDriver
	// 同口径，改默认值必须同时改两处，否则「没配 driver 的环境」在 server 与工厂里
	// 会落到不同后端）。
	Driver string
	// Prefix 是 key 顶层命名空间，空取 DefaultPrefix。
	Prefix string
	// Dir / TmpDir 分别来自 platform.blobs.dir / tmp_dir，由使用方给默认值：本包
	// 不猜 ~/.k8s-ai/blobs 与 /data/blobs 哪个是对的（§14）。
	Dir    string
	TmpDir string
	// MaxBytes 是单对象上限，<=0 取 DefaultMaxObjectBytes。
	MaxBytes int64

	// Endpoint / Region / UseTLS / PathStyle / Bucket / AccessKey / SecretKey /
	// CreateBucket / PresignTTL 只在 driver=s3 时生效。
	Endpoint     string
	Region       string
	UseTLS       bool
	PathStyle    bool
	Bucket       string
	AccessKey    string
	SecretKey    string
	CreateBucket bool
	PresignTTL   time.Duration

	// Replicas 是当前部署的应用副本数（>1 时 dir 后端会读到没写过的文件）。
	// 0 表示未知：不告警，避免在单测与 CLI 上刷无用 WARN。
	Replicas int
}

// New 按 cfg.Driver 构造后端，供 server 装配使用（四种 driver 都允许）。
//
// openDB 是 db 后端的注入函数：对象元数据登记表 objects 的 SQL 单源在 internal/store，
// 而本包禁止 import 它（tests/arch 门禁 TestObjectStoreDoesNotDependOnStore），所以
// 「怎么拿到那个 *store.ObjectContentStore」只能由装配层决定并在真正需要时交进来。
// driver != db 时 openDB 不会被调用——传 nil 也是安全的，这就是惰性注入的意义：
// 用 dir/s3 的部署不必为了构造一个后端先去开平台库连接池。
func New(cfg Config, openDB func() (Store, error)) (Store, error) {
	return newStore(cfg, openDB, false)
}

// NewCLI 构造 CLI 侧的后端，白名单只有 off 与 dir。
//
// 理由不是「CLI 不许用对象存储」，而是 CLI 的交付形态就是一个可执行文件加一个
// ~/.k8s-ai 目录（§16）：让它默认依赖一个 MySQL 或一个桶，等于让离线巡检先看运维有
// 没有准备好中间件。db 需要平台库连接、s3 需要桶与凭据，两者在 CLI 里都没有可靠的
// 来源与失败回退路径，所以在装配入口就硬拒，而不是等第一次上传才报一句不可用。
//
// 这条校验不能下放到 config 的 Validate：那里没有「当前是 server 还是 CLI」的上下文，
// 把白名单写进配置文件校验会让 server 也拒掉 s3。
func NewCLI(cfg Config) (Store, error) {
	return newStore(cfg, nil, true)
}

func newStore(cfg Config, openDB func() (Store, error), cli bool) (Store, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driver == "" {
		driver = DriverDB
	}
	if cli && driver != DriverOff && driver != DriverDir {
		return nil, fmt.Errorf("%w: CLI supports only platform.blobs.driver=%s|%s, got %q "+
			"(a CLI run must not depend on the platform database or an object bucket; see 方案 15 §16)",
			ErrUnsupported, DriverOff, DriverDir, cfg.Driver)
	}

	var store Store
	var err error
	switch driver {
	case DriverOff:
		store = NewDisabled()
	case DriverDir:
		store, err = NewDir(cfg.Dir, cfg.Prefix, cfg.MaxBytes)
	case DriverS3:
		store, err = NewS3(S3Config{
			Endpoint:     cfg.Endpoint,
			Region:       cfg.Region,
			UseTLS:       cfg.UseTLS,
			PathStyle:    cfg.PathStyle,
			Bucket:       cfg.Bucket,
			AccessKey:    cfg.AccessKey,
			SecretKey:    cfg.SecretKey,
			Prefix:       cfg.Prefix,
			TmpDir:       cfg.TmpDir,
			MaxBytes:     cfg.MaxBytes,
			CreateBucket: cfg.CreateBucket,
			PresignTTL:   cfg.PresignTTL,
		})
	case DriverDB:
		if openDB == nil {
			return nil, fmt.Errorf("%w: driver=%s needs the platform database backend but none was injected",
				ErrKeyInvalid, DriverDB)
		}
		store, err = openDB()
	default:
		return nil, fmt.Errorf("%w: platform.blobs.driver must be %s|%s|%s|%s, got %q",
			ErrKeyInvalid, DriverOff, DriverDB, DriverDir, DriverS3, cfg.Driver)
	}
	if err != nil {
		// 不叠包装：NewDir/NewS3 的错误里已点名具体配置键，再套一层只稀释信息。
		return nil, err
	}
	warnReplicas(cfg, driver)
	return store, nil
}

// warnReplicas 给「多副本 + dir 后端」这个致命组合留一条启动期告警（§9.4）。
//
// dir 的正文在本地卷上，副本之间不共享：A 副本写入的对象，B 副本读就是 ErrNotFound，
// 而且读得到与否取决于落在哪个副本——这种故障只在扩容之后才暴露，事后看日志很难归因
// 到「当初选了 dir」。这里只 WARN 不 fail：单容器挂 PVC 的多进程部署是合法形态，
// 是不是真的多副本由现场决定，装配层没有权限替它否决。
func warnReplicas(cfg Config, driver string) {
	if cfg.Replicas > 1 && driver == DriverDir {
		slog.Warn("objectstore: directory backend is not shared across replicas; switch platform.blobs.driver to s3 before scaling out",
			"driver", driver, "replicas", cfg.Replicas, "dir", cfg.Dir)
	}
}
