package objectstore_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// stubStore 用来验证注入路径：工厂只认 openDB 交回来的 Store，不关心它背后是什么。
type stubStore struct {
	objectstore.Store
	driver string
}

func (s stubStore) Driver() string { return s.driver }

func TestFactoryDispatchesEveryDriver(t *testing.T) {
	dirRoot := t.TempDir()
	cases := []struct {
		name   string
		cfg    objectstore.Config
		wantDB bool // 是否要求调用 openDB
		expect string
	}{
		{"off", objectstore.Config{Driver: "off"}, false, objectstore.DriverOff},
		{"dir", objectstore.Config{Driver: "dir", Dir: dirRoot}, false, objectstore.DriverDir},
		{"dir 空值走默认 db", objectstore.Config{}, true, "stub-db"},
		{"db", objectstore.Config{Driver: "db"}, true, "stub-db"},
		{"s3", objectstore.Config{Driver: "s3", Endpoint: "minio.local:9000", Region: "us-east-1",
			Bucket: "k8s-ai", AccessKey: "ak", SecretKey: "sk", TmpDir: t.TempDir()}, false, objectstore.DriverS3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			openDB := func() (objectstore.Store, error) {
				called = true
				return stubStore{Store: objectstore.NewDisabled(), driver: "stub-db"}, nil
			}
			st, err := objectstore.New(tc.cfg, openDB)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer st.Close()
			if st.Driver() != tc.expect {
				t.Fatalf("Driver = %q, want %q", st.Driver(), tc.expect)
			}
			if called != tc.wantDB {
				t.Errorf("openDB 被调用 = %v, want %v（非 db 后端不该为了构造去开平台库）", called, tc.wantDB)
			}
		})
	}
}

func TestFactoryRejectsBadConfig(t *testing.T) {
	t.Run("未知 driver", func(t *testing.T) {
		_, err := objectstore.New(objectstore.Config{Driver: "minio"}, func() (objectstore.Store, error) {
			return nil, errors.New("不该被调用")
		})
		if !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Fatalf("未知 driver = %v, want ErrKeyInvalid", err)
		}
		if !strings.Contains(err.Error(), "platform.blobs.driver") {
			t.Errorf("错误要点名配置键: %q", err.Error())
		}
	})
	t.Run("db 缺注入", func(t *testing.T) {
		_, err := objectstore.New(objectstore.Config{Driver: "db"}, nil)
		if !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Fatalf("db 无注入 = %v, want ErrKeyInvalid", err)
		}
	})
	t.Run("dir 缺根目录", func(t *testing.T) {
		_, err := objectstore.New(objectstore.Config{Driver: "dir"}, nil)
		if !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Fatalf("dir 无根目录 = %v, want ErrKeyInvalid", err)
		}
	})
	t.Run("openDB 报错原样透出", func(t *testing.T) {
		sentinel := errors.New("platform database is down")
		_, err := objectstore.New(objectstore.Config{Driver: "db"}, func() (objectstore.Store, error) {
			return nil, sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("注入错误被改写了: %v", err)
		}
	})
}

// TestFactoryCLIRefusesSharedBackends 锁 CLI 白名单：一次离线巡检不该因为「没准备好
// MySQL 或桶」而失败，也不该悄悄写进一个只有 server 才读得到的地方（§16）。
func TestFactoryCLIRefusesSharedBackends(t *testing.T) {
	for _, driver := range []string{"db", "s3"} {
		t.Run("CLI 拒绝 "+driver, func(t *testing.T) {
			_, err := objectstore.NewCLI(objectstore.Config{Driver: driver, Dir: t.TempDir(),
				Endpoint: "minio.local:9000", Bucket: "b", AccessKey: "ak", SecretKey: "sk"})
			if !errors.Is(err, objectstore.ErrUnsupported) {
				t.Fatalf("NewCLI(%s) = %v, want ErrUnsupported", driver, err)
			}
			if !strings.Contains(err.Error(), "platform.blobs.driver") {
				t.Errorf("错误要点名配置键: %q", err.Error())
			}
		})
	}
	t.Run("CLI 允许 off", func(t *testing.T) {
		st, err := objectstore.NewCLI(objectstore.Config{Driver: "off"})
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if st.Driver() != objectstore.DriverOff {
			t.Fatalf("Driver = %q", st.Driver())
		}
	})
	t.Run("CLI 允许 dir", func(t *testing.T) {
		st, err := objectstore.NewCLI(objectstore.Config{Driver: "dir", Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if st.Driver() != objectstore.DriverDir {
			t.Fatalf("Driver = %q", st.Driver())
		}
	})
}

// captureHandler 只收集 WARN 行：标准库没有 slog.HandlerFunc，要捕获日志就得自己实现
// 这四五个方法（用例是串行的，不需要加锁）。
type captureHandler struct{ lines []string }

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level != slog.LevelWarn {
		return nil
	}
	line := r.Message
	r.Attrs(func(a slog.Attr) bool {
		line += " " + a.Key + "=" + a.Value.String()
		return true
	})
	h.lines = append(h.lines, line)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// TestFactoryWarnsOnSharedDirectoryWithReplicas 锁多副本 + dir 的启动期告警：这个组合
// 的故障（B 副本读不到 A 副本写的对象）只在扩容之后才暴露，必须提前说。
func TestFactoryWarnsOnSharedDirectoryWithReplicas(t *testing.T) {
	warned := func(replicas int, driver string) []string {
		t.Helper()
		handler := &captureHandler{}
		previous := slog.Default()
		slog.SetDefault(slog.New(handler))
		t.Cleanup(func() { slog.SetDefault(previous) })

		cfg := objectstore.Config{Driver: driver, Dir: t.TempDir(), Replicas: replicas}
		if driver == objectstore.DriverS3 {
			// 这条用例只关心 WARN 该不该发，NewS3 不联网，给一份形状合法的配置就够。
			cfg.Endpoint = "minio.test.svc:9000"
			cfg.Region = "us-east-1"
			cfg.PathStyle = true
			cfg.Bucket = "k8s-ai-objects"
			cfg.AccessKey = "ak-test"
			cfg.SecretKey = "sk-test"
			cfg.TmpDir = t.TempDir()
		}
		st, err := objectstore.New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = st.Close()
		return handler.lines
	}

	lines := warned(3, objectstore.DriverDir)
	if len(lines) != 1 || !strings.Contains(lines[0], "objectstore:") || !strings.Contains(lines[0], "replicas=3") {
		t.Fatalf("多副本 dir 必须告警一次，got %v", lines)
	}
	if got := warned(1, objectstore.DriverDir); len(got) != 0 {
		t.Errorf("单副本不该告警: %v", got)
	}
	if got := warned(0, objectstore.DriverDir); len(got) != 0 {
		t.Errorf("副本数未知时不该告警: %v", got)
	}
	if got := warned(3, objectstore.DriverS3); len(got) != 0 {
		t.Errorf("s3 后端不该报 dir 的告警: %v", got)
	}
}

// TestFactoryOffBackendIsUsableWithoutAnyDependency 确认 off 后端拿到就能用（写必报
// ErrDisabled），这是「对象存储故障/未启用不影响基础业务」的装配起点。
func TestFactoryOffBackendIsUsableWithoutAnyDependency(t *testing.T) {
	st, err := objectstore.New(objectstore.Config{Driver: "off"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.Put(context.Background(), objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"u-1"}, Ext: ".md",
	}, strings.NewReader("x"))
	if !errors.Is(err, objectstore.ErrDisabled) {
		t.Fatalf("off 后端 Put = %v, want ErrDisabled", err)
	}
	if err := st.(objectstore.Maintainer).Ensure(context.Background()); !errors.Is(err, objectstore.ErrDisabled) {
		t.Fatalf("off 后端 Ensure = %v, want ErrDisabled", err)
	}
	// 顺带确认时间常量没被改动：预签名上限是 §13.2 的口径。
	if objectstore.MaxPresignTTL > 15*time.Minute {
		t.Errorf("MaxPresignTTL = %s, 不得超过 15m", objectstore.MaxPresignTTL)
	}
}
