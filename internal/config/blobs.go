package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// blob 对象后端取值（platform.blobs.driver，P8.1 / D-32 / ADR-043）。
const (
	// BlobDriverOff 关闭二进制产物通道：只保留结构化结论，不写任何对象。
	BlobDriverOff = "off"
	// BlobDriverDB 是默认后端：正文落平台库 objects 表的 payload 列（schema v6）。
	// 默认取它，保证没改配置的环境升级即无感。
	BlobDriverDB = "db"
	// BlobDriverDir 本地目录：单机/离线交付常用，跨副本不共享，扩副本前必须切 s3。
	BlobDriverDir = "dir"
	// BlobDriverS3 是 S3 兼容对象存储（MinIO/Ceph/厂商 S3）。
	BlobDriverS3 = "s3"
)

// blob 配置面的硬上限：超限直接 validate 失败，不留到运行期第一次上传才炸。
const (
	maxBlobPrefixLen  = 32
	maxBlobPresignTTL = 24 * time.Hour
	maxBlobObjectSize = int64(256) << 20
)

// blobPrefixPattern 约束对象键首段：小写字母数字开头，只允许 - 与 _。
// key 的防穿越闸门在 internal/objectstore 的 ValidateKey（三后端共享），这里只把
// 配置面关严，避免把一个带斜杠或大写的段当 prefix 传下去。
var blobPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// PlatformBlobsConfig 控制二进制对象后端（附件、报告产物、可选的会话正文外置）。
//
// 归属：与 platform.store 同级的**启动级形态参数**——换后端必须改配置文件并重启，
// 因此刻意不登记进 settings 动态键白名单、配置中心页面也不展示（方案 15 §8）：
// v1.51 已把动态键的环境变量覆盖面冻结为 LLM 四键 + ops 四键（ADR-040 v1.2）。
//
// 环境变量沿用既有机制（SetEnvPrefix("K8S_AI") + "."→"_" + AutomaticEnv），
// 实际名字是 K8S_AI_PLATFORM_BLOBS_DRIVER 这一族；每个键都有默认值，viper 才能在
// Unmarshal 时把只来自环境的值合并进来（与 platform.store.dsn 同一结论）。
type PlatformBlobsConfig struct {
	// Driver 是后端选择：off | db | dir | s3，默认 db。
	Driver string `mapstructure:"driver"`
	// Prefix 是对象键首段（e.g. k8sai），参与 key 拼接，≤32 字符。
	Prefix string `mapstructure:"prefix"`
	// Dir 是 driver=dir 的根目录；留空由使用方按场景取默认（容器 /data/blobs、
	// CLI ~/.k8s-ai/blobs），配置面不猜这个默认值。
	Dir string `mapstructure:"dir"`
	// TmpDir 是上传落盘的中转目录（driver=s3 先把正文流式落到这里算摘要，再单次 PUT，
	// 方案 15 §14）。留空取 os.TempDir()：容器里 /tmp 已是 emptyDir，显式键是为现场把
	// 临时盘指到大容量卷。与 Dir 同口径——配置面只做形状校验，可写性交给启动期 Ensure。
	TmpDir string `mapstructure:"tmp_dir"`
	// Endpoint 是 driver=s3 的服务地址：host:port，不带 scheme。
	Endpoint string `mapstructure:"endpoint"`
	// Region 是 S3 签名区域；MinIO 常见 us-east-1。
	Region string `mapstructure:"region"`
	// UseTLS 控制是否走 https 访问 endpoint。
	UseTLS bool `mapstructure:"use_tls"`
	// PathStyle 用路径式寻址（bucket 进 path）。MinIO/Ceph 必须 true。
	PathStyle bool `mapstructure:"path_style"`
	// Bucket 是桶名。
	Bucket string `mapstructure:"bucket"`
	// AccessKey/SecretKey 只应经环境变量/Secret 注入；配置文件里出现明文只 WARN
	// （不 fail，便于灰度与现场排障），见 Diagnostics。
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	// CreateBucket 在启动期幂等建桶；无建桶权限的现场置 false 并预建桶。
	CreateBucket bool `mapstructure:"create_bucket"`
	// PresignTTL 是预签名下载链接有效期，上限 24h。
	PresignTTL time.Duration `mapstructure:"presign_ttl"`
	// MaxObjectSize 是单对象上限（e.g. 32MiB），上限 256MiB，超限返回 ErrTooLarge。
	MaxObjectSize string `mapstructure:"max_object_size"`
	// SessionData 打开会话正文外置；默认关（db 后端下开了等于把大正文换个表塞回 MySQL）。
	SessionData bool `mapstructure:"session_data"`
	// GCGracePeriod 是 GC 宽限期：只回收超过该时长未被引用的对象，避开未提交事务竞争。
	GCGracePeriod time.Duration `mapstructure:"gc_grace_period"`
}

// EffectiveDriver 返回归一化后的后端取值（空值按默认后端 db）。
func (b PlatformBlobsConfig) EffectiveDriver() string {
	driver := strings.ToLower(strings.TrimSpace(b.Driver))
	if driver == "" {
		return BlobDriverDB
	}
	return driver
}

// Enabled 报告二进制产物通道是否开启（driver=off 时附件与报告不落对象）。
func (b PlatformBlobsConfig) Enabled() bool {
	return b.EffectiveDriver() != BlobDriverOff
}

// IsS3 报告是否使用 S3 兼容对象存储。
func (b PlatformBlobsConfig) IsS3() bool {
	return b.EffectiveDriver() == BlobDriverS3
}

// Validate 校验 blob 配置（P8.1）。规则与 deploy/install.sh 的参数校验共用同一判据：
// s3 缺 endpoint/bucket/access_key/secret_key 任一项即拒绝，避免出现「配了桶却连不上」
// 的假配置；install.sh 额外负责 ENABLE_MINIO=true 必须 driver=s3 这条（那是安装期参数，
// 不是运行时配置键）。
func (b PlatformBlobsConfig) Validate() error {
	switch driver := b.EffectiveDriver(); driver {
	case BlobDriverOff, BlobDriverDB, BlobDriverDir:
		// dir 后端刻意不校验目录是否存在/可写：启动期 Ensure 才是权威判定（方案 15 §9），
		// 配置面只做形状校验，避免 validate 在「有/无该文件系统」两种环境下结论不一致。
	case BlobDriverS3:
		for _, field := range []struct{ key, value string }{
			{"platform.blobs.endpoint", b.Endpoint},
			{"platform.blobs.bucket", b.Bucket},
			{"platform.blobs.access_key", b.AccessKey},
			{"platform.blobs.secret_key", b.SecretKey},
		} {
			if strings.TrimSpace(field.value) == "" {
				key := strings.ToUpper(strings.SplitN(field.key, ".", 3)[2])
				return fmt.Errorf("%s is required when platform.blobs.driver=s3 (inject it via Secret, e.g. K8S_AI_PLATFORM_BLOBS_%s)",
					field.key, key)
			}
		}
	default:
		return fmt.Errorf("platform.blobs.driver must be %s|%s|%s|%s, got %q",
			BlobDriverOff, BlobDriverDB, BlobDriverDir, BlobDriverS3, b.Driver)
	}

	prefix := strings.TrimSpace(b.Prefix)
	if prefix == "" {
		return fmt.Errorf("platform.blobs.prefix must not be empty")
	}
	if len(prefix) > maxBlobPrefixLen {
		return fmt.Errorf("platform.blobs.prefix must be at most %d characters, got %d", maxBlobPrefixLen, len(prefix))
	}
	if !blobPrefixPattern.MatchString(prefix) {
		return fmt.Errorf("platform.blobs.prefix must match %s, got %q", blobPrefixPattern.String(), prefix)
	}

	if b.PresignTTL <= 0 {
		return fmt.Errorf("platform.blobs.presign_ttl must be > 0")
	}
	if b.PresignTTL > maxBlobPresignTTL {
		return fmt.Errorf("platform.blobs.presign_ttl must be at most %s, got %s", maxBlobPresignTTL, b.PresignTTL)
	}

	size, err := ParseByteSize(b.MaxObjectSize)
	if err != nil {
		return fmt.Errorf("platform.blobs.max_object_size: %w", err)
	}
	if size <= 0 {
		return fmt.Errorf("platform.blobs.max_object_size must be > 0")
	}
	if size > maxBlobObjectSize {
		return fmt.Errorf("platform.blobs.max_object_size must be at most %d bytes (256MiB), got %d", maxBlobObjectSize, size)
	}

	if b.GCGracePeriod <= 0 {
		return fmt.Errorf("platform.blobs.gc_grace_period must be > 0")
	}
	return nil
}

// MaxObjectSizeBytes 解析 max_object_size，供 blobstore 与上传入口做容量闸门。
func (b PlatformBlobsConfig) MaxObjectSizeBytes() (int64, error) {
	return ParseByteSize(b.MaxObjectSize)
}

// ParseByteSize 解析容量字符串：纯数字按字节，支持 KiB/K、MiB/M、GiB/G、TiB/T
// （一律 1024 进制，与运维口头习惯一致），大小写与中间空格都不敏感。
func ParseByteSize(s string) (int64, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, fmt.Errorf("must not be empty (e.g. 32MiB)")
	}
	multiplier := int64(1)
	digits := raw
	if idx := strings.IndexFunc(raw, func(r rune) bool {
		return (r < '0' || r > '9') && r != '.'
	}); idx > 0 {
		digits = strings.TrimSpace(raw[:idx])
		switch strings.ToLower(strings.Join(strings.Fields(raw[idx:]), "")) {
		case "b":
		case "kib", "k":
			multiplier = 1 << 10
		case "mib", "m":
			multiplier = 1 << 20
		case "gib", "g":
			multiplier = 1 << 30
		case "tib", "t":
			multiplier = 1 << 40
		default:
			return 0, fmt.Errorf("unknown size unit %q (use B/KiB/MiB/GiB)", raw[idx:])
		}
	}
	value, err := strconv.ParseFloat(digits, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid size %q (e.g. 32MiB)", raw)
	}
	return int64(value * float64(multiplier)), nil
}

// blobsConfigWarnings 给配置文件里的 blob 段做软告警（Diagnostics 用，不阻断启动）：
// 凭据明文出现在文件里提示改走 Secret 注入；db 后端开会话外置给出选型提醒。
func blobsConfigWarnings(leaves map[string]any) []string {
	var warnings []string
	for _, key := range []string{"platform.blobs.access_key", "platform.blobs.secret_key"} {
		if value, ok := leaves[key].(string); ok && strings.TrimSpace(value) != "" {
			warnings = append(warnings, fmt.Sprintf(
				"%s 在配置文件里非空：blob 凭据应只经 K8S_AI_PLATFORM_%s / Secret 注入",
				key, strings.ToUpper(strings.SplitN(key, ".", 3)[2])))
		}
	}
	driver, _ := leaves["platform.blobs.driver"].(string)
	if sessionData, ok := leaves["platform.blobs.session_data"].(bool); ok && sessionData &&
		strings.ToLower(strings.TrimSpace(driver)) == BlobDriverDB {
		warnings = append(warnings, "platform.blobs.session_data=true 但 driver=db：会话正文外置在 db 后端下只是换个表塞回 MySQL，建议 driver=dir 或 s3")
	}
	return warnings
}
