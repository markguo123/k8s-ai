package objectstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func testSHA(seed byte) string {
	sum := sha256.Sum256([]byte{seed})
	return hex.EncodeToString(sum[:])
}

func TestValidateKeyAccepts(t *testing.T) {
	valid := []string{
		"k8sai/reports/cluster-1/scan-2/" + testSHA(1) + ".md",
		"k8sai/attachments/user-7/sess-3/" + testSHA(2),
		"k8sai/exports/" + testSHA(3) + ".json",
		"k8sai/sessions/" + testSHA(4) + ".jsonl",
	}
	for _, key := range valid {
		if err := ValidateKey(key); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", key, err)
		}
	}
}

// 穿越与畸形 key 必须全部落在 ValidateKey 这一处闸门上：dir 后端拿它拼路径，s3 后端拿它
// 拼对象名，任何一条漏过去都是「读到我/别人桶里别的东西」。
func TestValidateKeyRejects(t *testing.T) {
	hash := testSHA(5)
	invalid := map[string]string{
		"empty":             "",
		"uppercase":         "K8sAI/reports/" + hash,
		"dotdot":            "k8sai/reports/../../etc/passwd" + hash,
		"dot segment":       "k8sai/reports/./" + hash,
		"leading slash":     "/k8sai/reports/" + hash,
		"trailing slash":    "k8sai/reports/" + hash + "/",
		"double slash":      "k8sai//reports/" + hash,
		"backslash":         `k8sai\reports\` + hash,
		"colon (windows)":   "k8sai/reports/C:" + hash,
		"drive letter":      "c:/k8sai/reports/" + hash,
		"url encoded":       "k8sai/reports/%2e%2e/" + hash,
		"space":             "k8sai/re ports/" + hash,
		"control char":      "k8sai/reports/\x01" + hash,
		"too long":          "k8sai/reports/" + strings.Repeat("a", 200) + "/" + hash,
		"bad category":      "k8sai/secrets/" + hash,
		"missing category":  "k8sai/" + hash,
		"hash not first":    "k8sai/reports/prefix-" + hash,
		"hash not hex":      "k8sai/reports/" + strings.Repeat("g", 64),
		"hash too short":    "k8sai/reports/" + strings.Repeat("0", 63),
	}
	for name, key := range invalid {
		if err := ValidateKey(key); !errors.Is(err, ErrKeyInvalid) {
			t.Errorf("%s: ValidateKey(%q) = %v, want ErrKeyInvalid", name, key, err)
		}
	}
}

func TestCategoryOf(t *testing.T) {
	if got := CategoryOf("k8sai/reports/c/s/" + testSHA(6)); got != CategoryReports {
		t.Fatalf("CategoryOf = %q, want %q", got, CategoryReports)
	}
	if got := CategoryOf("k8sai/nope/" + testSHA(7)); got != "" {
		t.Fatalf("CategoryOf on unknown category = %q, want empty", got)
	}
}

func TestBuildKey(t *testing.T) {
	sum := testSHA(8)
	key, err := BuildKey("", CategoryReports, []string{"cluster-1", "scan-2"}, sum, "MD")
	if err != nil {
		t.Fatalf("BuildKey: %v", err)
	}
	if want := "k8sai/reports/cluster-1/scan-2/" + sum + ".md"; key != want {
		t.Fatalf("BuildKey = %q, want %q", key, want)
	}
	if err := ValidateKey(key); err != nil {
		t.Fatalf("BuildKey output must satisfy ValidateKey: %v", err)
	}
	// scope 为空会让「首段属于本项目 prefix 才允许删」这条 GC 判据失去依据。
	if _, err := BuildKey(DefaultPrefix, CategoryExports, nil, sum, ""); !errors.Is(err, ErrKeyInvalid) {
		t.Fatalf("BuildKey with empty scope = %v, want ErrKeyInvalid", err)
	}
	if _, err := BuildKey(DefaultPrefix, "secrets", []string{"u"}, sum, ""); !errors.Is(err, ErrKeyInvalid) {
		t.Fatalf("BuildKey with unknown category = %v, want ErrKeyInvalid", err)
	}
	if _, err := BuildKey(DefaultPrefix, CategoryExports, []string{"u"}, strings.Repeat("0", 63), ""); !errors.Is(err, ErrKeyInvalid) {
		t.Fatalf("BuildKey with short digest = %v, want ErrKeyInvalid", err)
	}
	if _, err := BuildKey(strings.Repeat("p", MaxPrefixLength+1), CategoryExports, []string{"u"}, sum, ""); !errors.Is(err, ErrKeyInvalid) {
		t.Fatalf("BuildKey with long prefix = %v, want ErrKeyInvalid", err)
	}
}

func TestNormalizeExt(t *testing.T) {
	for in, want := range map[string]string{"md": ".md", ".PNG": ".png", "": "", " .yaml": ".yaml"} {
		got, err := NormalizeExt(in)
		if err != nil {
			t.Fatalf("NormalizeExt(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("NormalizeExt(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{".", "..", ".a" + strings.Repeat("b", 20), ".html; charset", ".exe|"} {
		if _, err := NormalizeExt(in); !errors.Is(err, ErrKeyInvalid) {
			t.Fatalf("NormalizeExt(%q) = nil, want ErrKeyInvalid", in)
		}
	}
}

func TestValidateListPrefix(t *testing.T) {
	for _, ok := range []string{"k8sai", "k8sai/", "k8sai/reports", "k8sai/reports/cluster-1/"} {
		if err := ValidateListPrefix(ok); err != nil {
			t.Errorf("ValidateListPrefix(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "/", "//", "..", "k8sai/../other", "k8sai//reports", `k8sai\reports`, "K8SAI", "k8s ai", "k8sai/reports/" + strings.Repeat("x", 200)} {
		if err := ValidateListPrefix(bad); !errors.Is(err, ErrKeyInvalid) {
			t.Errorf("ValidateListPrefix(%q) = %v, want ErrKeyInvalid", bad, err)
		}
	}
}

func TestNormalizePrefix(t *testing.T) {
	for _, in := range []string{"", "   ", "k8sai/"} {
		got, err := NormalizePrefix(in)
		if err != nil {
			t.Fatalf("NormalizePrefix(%q): %v", in, err)
		}
		if got != DefaultPrefix {
			t.Fatalf("NormalizePrefix(%q) = %q, want %q", in, got, DefaultPrefix)
		}
	}
	if got, err := NormalizePrefix("k8s-ai-prod"); err != nil || got != "k8s-ai-prod" {
		t.Fatalf("NormalizePrefix = %q, %v", got, err)
	}
	if _, err := NormalizePrefix("bad prefix"); !errors.Is(err, ErrKeyInvalid) {
		t.Fatalf("NormalizePrefix(\"bad prefix\") = %v, want ErrKeyInvalid", err)
	}
}

func TestMediaTypeResolution(t *testing.T) {
	if got := ResolveMediaType("", ".md"); got != "text/markdown; charset=utf-8" {
		t.Fatalf("ResolveMediaType md = %q", got)
	}
	if got := ResolveMediaType("", ".unknown-ext"); got != OctetStream {
		t.Fatalf("unknown extension should fall back to octet-stream, got %q", got)
	}
	if got := ResolveMediaType("application/x-custom", ".md"); got != "application/x-custom" {
		t.Fatalf("explicit media type must win, got %q", got)
	}
}

func TestStreamHash(t *testing.T) {
	content := strings.Repeat("k8s-ai", 4096)
	want := sha256.Sum256([]byte(content))
	stream := NewStreamHash(strings.NewReader(content), 1<<20)
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if string(got) != content {
		t.Fatal("stream must pass bytes through unchanged")
	}
	if stream.SHA256() != hex.EncodeToString(want[:]) {
		t.Fatalf("digest = %s, want %s", stream.SHA256(), hex.EncodeToString(want[:]))
	}
	if stream.Size() != int64(len(content)) {
		t.Fatalf("size = %d, want %d", stream.Size(), len(content))
	}
}

// 超限必须在读到的那一刻断掉：先读满再判长度等于把闸门交给攻击者控制的内存占用。
func TestStreamHashRejectsOversize(t *testing.T) {
	stream := NewStreamHash(strings.NewReader(strings.Repeat("a", 4096)), 100)
	_, err := io.ReadAll(stream)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize read = %v, want ErrTooLarge", err)
	}
	if stream.Size() > 101 {
		t.Fatalf("oversize read buffered %d bytes, want at most limit+1", stream.Size())
	}
}

func TestReadAllHashed(t *testing.T) {
	payload, sum, err := ReadAllHashed(strings.NewReader("hello"), 10)
	if err != nil {
		t.Fatalf("ReadAllHashed: %v", err)
	}
	if string(payload) != "hello" {
		t.Fatalf("payload = %q", payload)
	}
	want := sha256.Sum256([]byte("hello"))
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("sum = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
	if _, _, err := ReadAllHashed(strings.NewReader("hello"), 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize = %v, want ErrTooLarge", err)
	}
}
