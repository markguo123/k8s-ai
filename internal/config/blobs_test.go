package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTempConfig 写一份临时配置文件并返回路径。
func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// defaultBlobs 取一份带默认值的 blob 配置。
//
// 范式说明：这里刻意不走 Load(Overrides) 构造非法配置——Load 末尾自带 Validate，
// 非法值会在加载阶段就返回错误，测不到规则本身的判定与文案。正确做法是先取默认
// 值，再直接改字段，最后显式调 PlatformBlobsConfig.Validate()。
func defaultBlobs(t *testing.T) PlatformBlobsConfig {
	t.Helper()
	cfg, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	return cfg.Platform.Blobs
}

// s3Valid 是 driver=s3 的最小合法四件套（endpoint/bucket/access_key/secret_key）。
func s3Valid(t *testing.T) PlatformBlobsConfig {
	t.Helper()
	b := defaultBlobs(t)
	b.Driver = BlobDriverS3
	b.Endpoint = "minio.internal:9000"
	b.Bucket = "k8s-ai"
	b.AccessKey = "ak"
	b.SecretKey = "sk"
	return b
}

// TestPlatformBlobsDefaults 锁默认形态：driver=db、不外链、容量与时长在口径内。
// 这条是「没改配置的老环境升级即无感」的锚点，改默认值必须先改方案 15 口径。
func TestPlatformBlobsDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Platform.Blobs
	if b.EffectiveDriver() != BlobDriverDB || b.IsS3() || !b.Enabled() {
		t.Errorf("默认后端应为 db: %+v", b)
	}
	if b.Prefix != "k8sai" || b.Region != "us-east-1" {
		t.Errorf("prefix/region 默认不符: %+v", b)
	}
	if !b.PathStyle || !b.CreateBucket {
		t.Errorf("path_style/create_bucket 默认应为 true（MinIO 现场最常见）: %+v", b)
	}
	if b.UseTLS || b.SessionData {
		t.Errorf("use_tls/session_data 默认应为 false: %+v", b)
	}
	if b.PresignTTL != 15*time.Minute {
		t.Errorf("presign_ttl 默认 15m, got %s", b.PresignTTL)
	}
	if b.GCGracePeriod != 168*time.Hour {
		t.Errorf("gc_grace_period 默认 168h, got %s", b.GCGracePeriod)
	}
	if size, err := b.MaxObjectSizeBytes(); err != nil || size != 32<<20 {
		t.Errorf("max_object_size 默认应为 32MiB, got %d err %v", size, err)
	}
	if b.Dir != "" {
		t.Errorf("dir 默认留空，由使用方按场景取默认: %q", b.Dir)
	}
	// tmp_dir 默认留空 = 用 os.TempDir()（容器里已是 emptyDir）；配置面不猜路径。
	if b.TmpDir != "" {
		t.Errorf("tmp_dir 默认应留空: %q", b.TmpDir)
	}
	if b.Endpoint != "" || b.AccessKey != "" || b.SecretKey != "" {
		t.Errorf("默认不得内置任何 endpoint/凭据: %+v", b)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}
}

// TestPlatformBlobsOffDisablesChannel 锁 driver=off 的语义：通道整体关闭且不要求凭据。
func TestPlatformBlobsOffDisablesChannel(t *testing.T) {
	b := defaultBlobs(t)
	b.Driver = " OFF "
	if b.EffectiveDriver() != BlobDriverOff || b.Enabled() || b.IsS3() {
		t.Errorf("driver=off 应关闭通道且大小写/空格不敏感: %+v", b)
	}
	if err := b.Validate(); err != nil {
		t.Errorf("off 后端不应要求凭据: %v", err)
	}
	// 零值配置（配置文件里整段省略）必须落在默认 db，否则老环境升级会静默丢产物。
	if got := (PlatformBlobsConfig{}).EffectiveDriver(); got != BlobDriverDB {
		t.Errorf("driver 留空应按默认 db, got %q", got)
	}
}

// TestPlatformBlobsS3RequiresAllFourFields 锁与 install.sh 共用的那条硬判据：
// s3 缺 endpoint/bucket/access_key/secret_key 任一项都必须拒绝，且报错点出缺哪个键
// 与对应的 Secret 注入名，避免「装了 MinIO 却连不上」这种要跑一次上传才暴露的假配置。
func TestPlatformBlobsS3RequiresAllFourFields(t *testing.T) {
	cases := []struct {
		key   string
		clear func(*PlatformBlobsConfig)
	}{
		{"platform.blobs.endpoint", func(b *PlatformBlobsConfig) { b.Endpoint = "   " }},
		{"platform.blobs.bucket", func(b *PlatformBlobsConfig) { b.Bucket = "" }},
		{"platform.blobs.access_key", func(b *PlatformBlobsConfig) { b.AccessKey = "  " }},
		{"platform.blobs.secret_key", func(b *PlatformBlobsConfig) { b.SecretKey = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			b := s3Valid(t)
			tc.clear(&b)
			err := b.Validate()
			if err == nil {
				t.Fatalf("缺 %s 应拒绝", tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("报错应点出 %s, got %v", tc.key, err)
			}
			if !strings.Contains(err.Error(), "K8S_AI_PLATFORM_BLOBS_") {
				t.Fatalf("报错应给出 Secret 注入用的环境变量名, got %v", err)
			}
		})
	}

	b := s3Valid(t)
	if err := b.Validate(); err != nil {
		t.Fatalf("四件套齐备应通过: %v", err)
	}
	if !b.IsS3() || !b.Enabled() {
		t.Fatalf("IsS3/Enabled 应为 true: %+v", b)
	}
}

// TestPlatformBlobsDriverWhitelist 锁 driver 白名单：拼错必须拒绝，不能静默回落。
func TestPlatformBlobsDriverWhitelist(t *testing.T) {
	for _, driver := range []string{BlobDriverOff, BlobDriverDB, BlobDriverDir, BlobDriverS3, "S3", " s3 "} {
		b := s3Valid(t)
		b.Driver = driver
		if err := b.Validate(); err != nil {
			t.Errorf("driver=%q 应合法: %v", driver, err)
		}
	}
	for _, driver := range []string{"minio", "mysql", "postgres", "本地", "d b"} {
		b := s3Valid(t)
		b.Driver = driver
		if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "platform.blobs.driver") {
			t.Errorf("driver=%q 应被白名单拒绝并点出键名, got %v", driver, err)
		}
	}
}

// TestPlatformBlobsFieldLimits 锁 prefix/时长/容量三处范围校验。
func TestPlatformBlobsFieldLimits(t *testing.T) {
	cases := []struct {
		name      string
		patch     func(*PlatformBlobsConfig)
		wantField string
	}{
		{"prefix 空", func(b *PlatformBlobsConfig) { b.Prefix = "  " }, "prefix"},
		{"prefix 超长", func(b *PlatformBlobsConfig) { b.Prefix = strings.Repeat("a", maxBlobPrefixLen+1) }, "prefix"},
		{"prefix 非法字符", func(b *PlatformBlobsConfig) { b.Prefix = "k8s/ai" }, "prefix"},
		{"prefix 首字符非法", func(b *PlatformBlobsConfig) { b.Prefix = "-k8sai" }, "prefix"},
		{"presign 零", func(b *PlatformBlobsConfig) { b.PresignTTL = 0 }, "presign_ttl"},
		{"presign 超 24h", func(b *PlatformBlobsConfig) { b.PresignTTL = 25 * time.Hour }, "presign_ttl"},
		{"容量单位不认识", func(b *PlatformBlobsConfig) { b.MaxObjectSize = "12XiB" }, "max_object_size"},
		{"容量超 256MiB", func(b *PlatformBlobsConfig) { b.MaxObjectSize = "512MiB" }, "max_object_size"},
		{"容量空", func(b *PlatformBlobsConfig) { b.MaxObjectSize = "" }, "max_object_size"},
		{"GC 非正", func(b *PlatformBlobsConfig) { b.GCGracePeriod = 0 }, "gc_grace_period"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := defaultBlobs(t)
			tc.patch(&b)
			if err := b.Validate(); err == nil || !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("应拒绝并报出 %s, got %v", tc.wantField, err)
			}
		})
	}
}

// TestParseByteSize 锁容量字符串解析：纯数字按字节，K/M/G/T 一律 1024 进制。
func TestParseByteSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"512", 512},
		{"1KiB", 1 << 10},
		{" 2MiB ", 2 << 20},
		{"1 GiB", 1 << 30},
		{"8M", 8 << 20},
		{"1t", 1 << 40},
		{"64b", 64},
	} {
		got, err := ParseByteSize(tc.in)
		if err != nil {
			t.Errorf("ParseByteSize(%q) 失败: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "   ", "abc", "12XiB", "-5", "MiB"} {
		if _, err := ParseByteSize(bad); err == nil {
			t.Errorf("ParseByteSize(%q) 应报错", bad)
		}
	}
}

// TestPlatformBlobsEnvOverride 锁部署侧注入通道与真实环境变量名（install.sh 与 P8.4
// 清单按这套名字写）。前缀 K8S_AI + "."→"_" 意味着 platform.blobs.* 对应
// K8S_AI_PLATFORM_BLOBS_*，不是 K8S_AI_BLOBS_*。
func TestPlatformBlobsEnvOverride(t *testing.T) {
	t.Setenv("K8S_AI_PLATFORM_BLOBS_DRIVER", "s3")
	t.Setenv("K8S_AI_PLATFORM_BLOBS_ENDPOINT", "minio.k8s-ai.svc.cluster.local:9000")
	t.Setenv("K8S_AI_PLATFORM_BLOBS_BUCKET", "k8s-ai-reports")
	t.Setenv("K8S_AI_PLATFORM_BLOBS_ACCESS_KEY", "ak-from-secret")
	t.Setenv("K8S_AI_PLATFORM_BLOBS_SECRET_KEY", "sk-from-secret")
	t.Setenv("K8S_AI_PLATFORM_BLOBS_TMP_DIR", "/var/tmp/k8s-ai-put")
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	b := cfg.Platform.Blobs
	if !b.IsS3() || b.Endpoint == "" || b.Bucket == "" || b.AccessKey != "ak-from-secret" || b.SecretKey != "sk-from-secret" {
		t.Fatalf("env 注入未生效: %+v", b)
	}
	if b.TmpDir != "/var/tmp/k8s-ai-put" {
		t.Fatalf("tmp_dir env 注入未生效: %q", b.TmpDir)
	}
}

// TestPlatformBlobsDirExpandsHome 锁 dir 的 ~ 展开（与 report.directory 同机制）。
func TestPlatformBlobsDirExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cfg, err := Load(LoadOptions{NoEnv: true, Overrides: map[string]any{
		"platform.blobs.driver": BlobDriverDir,
		"platform.blobs.dir":    "~/.k8s-ai/blobs",
	}})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if want := filepath.Join(home, ".k8s-ai", "blobs"); cfg.Platform.Blobs.Dir != want {
		t.Fatalf("dir = %q, want %q", cfg.Platform.Blobs.Dir, want)
	}
}

// TestPlatformBlobsCLITemplateLoadsDirBackend 锁 CLI 模板自带 dir 后端：
// 一个二进制丢到 master 节点就能用，不依赖桶，也不该冒任何配置告警。
func TestPlatformBlobsCLITemplateLoadsDirBackend(t *testing.T) {
	path := writeTempConfig(t, CLITemplate)
	cfg, err := Load(LoadOptions{ConfigFile: path, NoEnv: true, CLI: true})
	if err != nil {
		t.Fatalf("load cli template: %v", err)
	}
	b := cfg.Platform.Blobs
	if b.EffectiveDriver() != BlobDriverDir {
		t.Fatalf("CLI 模板应默认 dir 后端, got %q", b.Driver)
	}
	if !strings.HasSuffix(filepath.ToSlash(b.Dir), "/.k8s-ai/blobs") {
		t.Fatalf("CLI 模板 dir 默认应落在用户目录, got %q", b.Dir)
	}
	warnings, err := Diagnostics(path, ValidateModeCLI)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("CLI 模板应零告警, got %v", warnings)
	}
}

// TestBlobsDiagnosticsWarnings 锁两条软告警：凭据明文进配置文件、db 后端开会话外置。
// 都是 WARN 不 fail（灰度可控），但绝不能一声不吭。
func TestBlobsDiagnosticsWarnings(t *testing.T) {
	path := writeTempConfig(t, ""+
		"platform:\n"+
		"  blobs:\n"+
		"    driver: db\n"+
		"    access_key: plaintext-ak\n"+
		"    session_data: true\n")
	warnings, err := Diagnostics(path, ValidateModeServer)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "platform.blobs.access_key") {
		t.Errorf("凭据明文应告警, got %v", warnings)
	}
	if !strings.Contains(joined, "platform.blobs.session_data") {
		t.Errorf("db 后端开 session_data 应告警, got %v", warnings)
	}

	clean := writeTempConfig(t, ""+
		"platform:\n"+
		"  blobs:\n"+
		"    driver: s3\n"+
		"    access_key: \"\"\n"+
		"    session_data: true\n")
	warnings, err = Diagnostics(clean, ValidateModeServer)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("凭据留空且非 db 后端应零告警, got %v", warnings)
	}
}
