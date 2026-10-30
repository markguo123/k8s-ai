package objectstore

import (
	"fmt"
	"regexp"
	"strings"
)

// key 规则（§5）：{prefix}/{category}/{scope...}/{sha256}{ext}
const (
	// DefaultPrefix 是 key 的顶层命名空间，也是 platform.blobs.prefix 的默认值。
	DefaultPrefix = "k8sai"
	// MaxKeyLength 沿用 colText 约定：MySQL 渲染成 VARCHAR(191)，utf8mb4 老行格式
	// 下 191*4=764 字节不踩 767 字节索引上限。超长直接拒绝，不做哈希截断。
	MaxKeyLength = 191
	// MaxPrefixLength 给 scope 段留出余量（现网 userID/sessionID 最长约 145）。
	MaxPrefixLength = 32
	// SHA256HexLen 是内容寻址段的固定长度。
	SHA256HexLen = 64
	// SystemActor 是系统任务写入的对象归属。
	SystemActor = "system"
	// DefaultMaxObjectBytes 是 platform.blobs.max_object_size 的默认值（32MiB）。
	DefaultMaxObjectBytes int64 = 32 << 20
)

// category 白名单。sessions 四期不写入（§8.4：会话正文不外置），但白名单位保留，
// 避免将来加类时改校验规则。
const (
	CategoryReports     = "reports"
	CategoryAttachments = "attachments"
	CategoryExports     = "exports"
	CategorySessions    = "sessions"
)

var categories = []string{CategoryReports, CategoryAttachments, CategoryExports, CategorySessions}

// Categories 返回白名单副本，供 config validate 与文档一致性测试消费。
func Categories() []string {
	out := make([]string, len(categories))
	copy(out, categories)
	return out
}

// ValidCategory 报告 category 是否在白名单内。
func ValidCategory(category string) bool {
	for _, c := range categories {
		if c == category {
			return true
		}
	}
	return false
}

var (
	keyRe = regexp.MustCompile(`^[a-z0-9._-]+(/[a-z0-9._-]+)*$`)
	// keySegmentRe 是单个路径段的字符集，供前缀校验按段复用（整串规则见 keyRe）。
	keySegmentRe = regexp.MustCompile(`^[a-z0-9._-]+$`)
	hashRe       = regexp.MustCompile(`^[0-9a-f]{64}`)
	shaRe        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	extRe        = regexp.MustCompile(`^\.[a-z0-9][a-z0-9.]{0,15}$`)
)

// IsSHA256 报告 s 是否为 64 位小写十六进制摘要。
func IsSHA256(s string) bool { return shaRe.MatchString(s) }

// ValidateKey 是三后端共享的唯一防穿越闸门（§5）：Store 的每个入口都必须先过它。
//
// 规则：字符集只允许 [a-z0-9._-] 与 /（因此天然拒绝前导/结尾 /、//、反斜杠、盘符、
// URL 编码残留、控制字符、大写与空格）；任何段不得是纯点（防 . 与 ..）；至少要有
// prefix/category/hash 三段；category 段必须在白名单内；尾段必须以 64 位十六进制
// sha256 开头（内容寻址不变量，GC 与去重依赖它）；总长 ≤191。
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty key", ErrKeyInvalid)
	}
	if len(key) > MaxKeyLength {
		return fmt.Errorf("%w: %d bytes exceeds limit %d", ErrKeyInvalid, len(key), MaxKeyLength)
	}
	if !keyRe.MatchString(key) {
		return fmt.Errorf("%w: %q violates [a-z0-9._-]+(/...)*", ErrKeyInvalid, key)
	}
	segments := strings.Split(key, "/")
	if len(segments) < 3 {
		return fmt.Errorf("%w: %q needs prefix/category/hash at least", ErrKeyInvalid, key)
	}
	for _, seg := range segments {
		if strings.Trim(seg, ".") == "" {
			return fmt.Errorf("%w: %q has a dot-only path segment", ErrKeyInvalid, key)
		}
	}
	if !ValidCategory(segments[1]) {
		return fmt.Errorf("%w: %q category %q not in whitelist %s",
			ErrKeyInvalid, key, segments[1], strings.Join(categories, "|"))
	}
	if last := segments[len(segments)-1]; !hashRe.MatchString(last) {
		return fmt.Errorf("%w: %q last segment must start with a %d-hex sha256",
			ErrKeyInvalid, key, SHA256HexLen)
	}
	return nil
}

// CategoryOf 取 key 的 category 段；key 不合法时返回空串（调用方应先 ValidateKey）。
func CategoryOf(key string) string {
	segments := strings.Split(key, "/")
	if len(segments) < 2 || !ValidCategory(segments[1]) {
		return ""
	}
	return segments[1]
}

// BuildKey 按实际内容摘要组装 key，是唯一允许生成 key 的入口。
//
// prefix 为空取 DefaultPrefix。scope 至少要有一段：四类 key 模板都带命名空间，允许空
// scope 会让 GC 的「首段属于本项目 prefix 才允许删」失去判断依据。组装完再整体过一遍
// ValidateKey——净化点始终只有那一处。
func BuildKey(prefix, category string, scope []string, sha256Hex, ext string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if len(prefix) > MaxPrefixLength {
		return "", fmt.Errorf("%w: prefix %q exceeds %d bytes", ErrKeyInvalid, prefix, MaxPrefixLength)
	}
	if !ValidCategory(category) {
		return "", fmt.Errorf("%w: category %q not in whitelist %s",
			ErrKeyInvalid, category, strings.Join(categories, "|"))
	}
	if len(scope) == 0 {
		return "", fmt.Errorf("%w: scope must not be empty", ErrKeyInvalid)
	}
	if !IsSHA256(sha256Hex) {
		return "", fmt.Errorf("%w: %q is not a %d-hex sha256", ErrKeyInvalid, sha256Hex, SHA256HexLen)
	}
	normalized, err := NormalizeExt(ext)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(scope)+3)
	parts = append(parts, prefix, category)
	parts = append(parts, scope...)
	parts = append(parts, sha256Hex+normalized)
	key := strings.Join(parts, "/")
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// NormalizeExt 统一扩展名：补前导点、转小写、限制字符集与长度。扩展名来自上传方，
// 属于用户可控输入，因此和 scope 一样必须过校验。
func NormalizeExt(ext string) (string, error) {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return "", nil
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if strings.Trim(ext, ".") == "" || !extRe.MatchString(ext) {
		return "", fmt.Errorf("%w: invalid extension %q", ErrKeyInvalid, ext)
	}
	return ext, nil
}

// NormalizePrefix 规范化并校验对象 key 的顶层 prefix：空取 DefaultPrefix，容忍首尾空白
// 与误写的尾随斜杠（"k8sai/" 拼出的 key 会带空段，必须在这里而不是拼接处拒掉）。
func NormalizePrefix(prefix string) (string, error) {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return DefaultPrefix, nil
	}
	if err := ValidateListPrefix(prefix); err != nil {
		return "", err
	}
	return prefix, nil
}

// ValidateListPrefix 校验 List 的前缀参数。
//
// 它不能复用 ValidateKey：合法前缀天然是「半个 key」（"k8sai"、"k8sai/reports" 都要能
// 列），拿完整 key 规则去卡会把它们一律拒掉。但防穿越那条红线一点不能松：字符集、
// 段内不得是纯点、总长上限全部沿用，否则目录后端的前缀扫描就是现成的穿越入口。
// 允许一个尾随斜杠（按目录前缀列）。
func ValidateListPrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("%w: empty list prefix", ErrKeyInvalid)
	}
	if len(prefix) > MaxKeyLength {
		return fmt.Errorf("%w: prefix %d bytes exceeds limit %d", ErrKeyInvalid, len(prefix), MaxKeyLength)
	}
	body := strings.TrimSuffix(prefix, "/")
	if body == "" {
		return fmt.Errorf("%w: list prefix %q must name at least one segment", ErrKeyInvalid, prefix)
	}
	for _, seg := range strings.Split(body, "/") {
		if !keySegmentRe.MatchString(seg) || strings.Trim(seg, ".") == "" {
			return fmt.Errorf("%w: list prefix %q has an invalid segment %q", ErrKeyInvalid, prefix, seg)
		}
	}
	return nil
}
