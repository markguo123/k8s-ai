package objectstore_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// s3TestConfig 给一份「形状合法」的 S3 配置。端点用不可解析的内网名：本文件的离线用例
// 要么根本不发请求（构造、预签名），要么必须证明「发不出去」被判成故障而不是「不存在」，
// 两种情况都不该依赖真实 DNS 与网络。
func s3TestConfig(t *testing.T) objectstore.S3Config {
	t.Helper()
	return objectstore.S3Config{
		Endpoint:   "minio.k8s-ai.svc.cluster.local:9000",
		Region:     "us-east-1",
		Bucket:     "k8s-ai-objects",
		AccessKey:  "ak-test",
		SecretKey:  "sk-test",
		PathStyle:  true,
		TmpDir:     t.TempDir(),
		MaxBytes:   1 << 20,
		PresignTTL: 5 * time.Minute,
	}
}

// TestNewS3RejectsBadConfigOffline 锁构造期校验：形状不对当场点名配置键，而不是等启动期
// Ensure 报一句「不可达」让人去猜是网络还是名字。
func TestNewS3RejectsBadConfigOffline(t *testing.T) {
	base := s3TestConfig(t)
	if _, err := objectstore.NewS3(base); err != nil {
		t.Fatalf("合法配置被拒: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*objectstore.S3Config)
	}{
		{"缺 endpoint", func(c *objectstore.S3Config) { c.Endpoint = "" }},
		{"endpoint 带 scheme", func(c *objectstore.S3Config) { c.Endpoint = "http://minio.local:9000" }},
		{"缺 bucket", func(c *objectstore.S3Config) { c.Bucket = "" }},
		{"桶名含大写", func(c *objectstore.S3Config) { c.Bucket = "K8s-AI" }},
		{"桶名过短", func(c *objectstore.S3Config) { c.Bucket = "ab" }},
		{"缺 access_key", func(c *objectstore.S3Config) { c.AccessKey = "" }},
		{"缺 secret_key", func(c *objectstore.S3Config) { c.SecretKey = "" }},
		// 多段前缀是合法的（要支持 k8sai/reports 这种共享桶），真正要挡的是穿越。
		{"prefix 带穿越", func(c *objectstore.S3Config) { c.Prefix = "a/../../b" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			_, err := objectstore.NewS3(cfg)
			if !errors.Is(err, objectstore.ErrKeyInvalid) {
				t.Fatalf("NewS3 = %v, want ErrKeyInvalid", err)
			}
			if !strings.Contains(err.Error(), "platform.blobs") {
				t.Errorf("错误必须点名配置键，got %q", err.Error())
			}
		})
	}
}

// TestNewS3ImplementsAllCapabilities 锁后端能力面：s3 是唯一实现 Presigner 的后端，
// 也是 Maintainer/Probe 的实现者（§13.2 的下载分流全靠这个断言）。
func TestNewS3ImplementsAllCapabilities(t *testing.T) {
	st, err := objectstore.NewS3(s3TestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.Driver() != objectstore.DriverS3 {
		t.Errorf("Driver = %q", st.Driver())
	}
	if _, ok := st.(objectstore.Maintainer); !ok {
		t.Error("s3 必须实现 Maintainer")
	}
	if _, ok := st.(objectstore.Probe); !ok {
		t.Error("s3 必须实现 Probe")
	}
	if _, ok := st.(objectstore.Presigner); !ok {
		t.Error("s3 必须实现 Presigner")
	}
}

// TestS3OfflineFailureIsNeverNotFound 是本文件最重要的一条：端点连不上时必须落在
// ErrUnavailable，且绝不能同时是 ErrNotFound。一旦把「连不上」报成「对象不存在」，§12 的
// 对账会把健康对象判成 missing，而那是个不可逆结论。
func TestS3OfflineFailureIsNeverNotFound(t *testing.T) {
	cfg := s3TestConfig(t)
	cfg.Endpoint = "127.0.0.1:1" // 立刻 ECONNREFUSED，不等拨号超时
	st, err := objectstore.NewS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := objectstore.DefaultPrefix + "/reports/u-1/" + strings.Repeat("a", 64) + ".md"

	requireUnavailable := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s 竟然成功了", name)
		}
		if !errors.Is(err, objectstore.ErrUnavailable) {
			t.Errorf("%s = %v, want ErrUnavailable", name, err)
		}
		if errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("%s = %v，把不可达报成了不存在", name, err)
		}
	}
	_, err = st.Stat(ctx, key)
	requireUnavailable("Stat", err)
	_, _, err = st.Get(ctx, key)
	requireUnavailable("Get", err)
	requireUnavailable("Delete", st.Delete(ctx, key))
	requireUnavailable("List", st.List(ctx, objectstore.DefaultPrefix+"/", func(objectstore.Ref) error { return nil }))
	requireUnavailable("Put", func() error {
		_, err := st.Put(ctx, objectstore.Spec{Category: objectstore.CategoryReports, Scope: []string{"u-1"}, Ext: ".md"}, strings.NewReader("payload"))
		return err
	}())
	requireUnavailable("Probe", st.(objectstore.Probe).Probe(ctx))
	requireUnavailable("Ensure", st.(objectstore.Maintainer).Ensure(ctx))

	// 上传失败后中转文件必须已经消失：这是 Put 里那个无条件 defer 的唯一证据，漏掉它就会
	// 在容器可写层里攒出一堆没人的附件正文。
	entries, err := os.ReadDir(cfg.TmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("Put 失败后中转目录残留 %d 个文件: %v", len(entries), entries)
	}
}

// TestS3PresignWorksOffline 锁两件事：预签名不发网络请求（Region 显式给值时 SDK 跳过
// GetBucketLocation），以及 TTL 一定被夹进 MaxPresignTTL（§13.2）。
func TestS3PresignWorksOffline(t *testing.T) {
	st, err := objectstore.NewS3(s3TestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	presigner := st.(objectstore.Presigner)
	ctx := context.Background()
	key := objectstore.DefaultPrefix + "/reports/u-1/" + strings.Repeat("b", 64) + ".md"

	// ttl<=0 回落到构造期值（5m）：调用方忘传 ttl 时不该失败，也不该拿到比配置更长的链接。
	signed, err := presigner.PresignGET(ctx, key, 0)
	if err != nil {
		t.Fatalf("PresignGET: %v", err)
	}
	if !strings.Contains(signed, "X-Amz-Expires=300") {
		t.Errorf("ttl=0 应取配置值 5m，got %s", signed)
	}
	for _, want := range []string{"minio.k8s-ai.svc.cluster.local:9000", "k8s-ai-objects", key, "X-Amz-Signature="} {
		if !strings.Contains(signed, want) {
			t.Errorf("预签名 URL 缺少 %q: %s", want, signed)
		}
	}

	// 超过上限的请求值一律夹到 15m：桶侧对已签发的 URL 没有任何补救手段。
	clamped, err := presigner.PresignGET(ctx, key, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clamped, "X-Amz-Expires=900") {
		t.Errorf("超过上限的 ttl 未被夹到 %s，got %s", objectstore.MaxPresignTTL, clamped)
	}
	short, err := presigner.PresignGET(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(short, "X-Amz-Expires=60") {
		t.Errorf("上限内的 ttl 应原样生效，got %s", short)
	}

	if _, err := presigner.PresignGET(ctx, "../etc/passwd", time.Minute); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Errorf("非法 key = %v, want ErrKeyInvalid", err)
	}
}

// ---------------------------------------------------------------------------
// 内存假 S3：只用 GET/PUT/HEAD/DELETE + ListObjectsV2 这几种真实 S3 一定会给的语义，
// 让读写删列四条路径在离线 CI 里也被钉住。真机门禁（TestS3BackendContract）负责证明
// 真桶的行为与这个假桶一致，两者缺一不可。
// ---------------------------------------------------------------------------

type fakeS3Entry struct {
	body []byte
	mod  time.Time
}

type fakeS3 struct {
	t *testing.T

	mu      sync.Mutex
	objects map[string]fakeS3Entry
	puts    int

	// denyMessage 非空时让**带 body 的读请求**（GET 对象与 LIST）回 403 + 该文本：用来造
	// 「SDK 错误串里带凭据」的场景。桶级与对象级 HEAD 一律放行——HEAD 按 HTTP 规定没有
	// body，我们写的 XML 会被 Go 丢掉，拿它做断言等于什么也没验。
	denyMessage string
	// streamingPuts 记下「到达时带 aws-chunked 签名框架」的 PUT 次数（见 decodeAWSChunked）。
	streamingPuts int
	// multipartRequests 记下 ?uploads 请求：分片上传一旦出现就说明 PutObject 走岔了路。
	multipartRequests int
	// md5Checks 记下假桶真正比对过 Content-MD5 的次数，0 就说明生产侧那个开关没生效。
	md5Checks int
	// listStatus/listCode 非零时让 LIST 返回指定错误，模拟桶没了。
	listStatus int
	listCode   string
	// extraKeys 是「桶里别人的对象」：GC 的差集判定全靠后端把它们挡在外面。
	extraKeys []string
}

func newFakeS3(t *testing.T) (*fakeS3, string) {
	t.Helper()
	f := &fakeS3{t: t, objects: map[string]fakeS3Entry{}, extraKeys: []string{
		"other-system/backups/dump.bin",         // 同桶里别的项目的对象
		objectstore.DefaultPrefix + "/reports/", // 控制台建目录留下的占位对象
	}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, strings.TrimPrefix(srv.URL, "http://")
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// 只挡 GET。HEAD 响应按 HTTP 规定不带 body，Go 会丢掉这里的 XML，用例就退化成「断言状态码
	// 兜底文案」，脱敏那部分变成空转；而 minio 取对象元数据走的正是 HEAD（Object.Stat() 内部
	// 调 StatObject），所以只有 LIST 这条 GET 路径能把 <Message> 送进错误串。
	// 桶级 HEAD 也放行，Ensure 才能正常过。
	if f.denyMessage != "" && r.Method == http.MethodGet {
		writeS3Error(w, http.StatusForbidden, "AccessDenied", f.denyMessage)
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(trimmed, "/")
	if bucket == "" {
		http.Error(w, "bucket missing in path-style request", http.StatusInternalServerError)
		return
	}

	switch {
	case r.Method == http.MethodGet && key == "" && r.URL.Query().Get("list-type") == "2":
		f.writeList(w, bucket, r.URL.Query().Get("prefix"))
	case r.Method == http.MethodHead && key == "":
		w.WriteHeader(http.StatusOK) // BucketExists
	case r.Method == http.MethodHead:
		entry, ok := f.objects[key]
		if !ok {
			// HEAD 不能带 body，SDK 会按状态码兜成 NoSuchKey。
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.body)))
		w.Header().Set("Last-Modified", entry.mod.UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"fake-etag"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet:
		entry, ok := f.objects[key]
		if !ok {
			writeS3Error(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ETag", `"fake-etag"`)
		// GET 上也得带 Last-Modified：真 S3 一定带，而 minio-go 会拿 GET 响应头与先前那次
		// HEAD 的结果做一致性比对，缺这个头就直接报 "Last-Modified time format is invalid"，
		// 正文根本读不出来。
		w.Header().Set("Last-Modified", entry.mod.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(entry.body)
	case r.Method == http.MethodPut && r.URL.Query().Has("uploads"):
		// §23.2 明确不做分片上传。这里不留情面地报 501：一旦 PutObject 改走 multipart，
		// ETag 会变成 <md5>-N，§12.2 的校验就没了着落，必须让用例先炸。
		f.multipartRequests++
		writeS3Error(w, http.StatusNotImplemented, "NotImplemented", "multipart upload is disabled by design")
	case r.Method == http.MethodPut:
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		body, framed, err := decodeAWSChunked(r, raw)
		if err != nil {
			writeS3Error(w, http.StatusBadRequest, "InvalidRequest", err.Error())
			return
		}
		if framed {
			f.streamingPuts++
		}
		// 摘要不符要按 S3 的样子回 BadDigest：生产侧开了 SendContentMd5，这里比对才算真的
		// 验过那一层，否则「发了 Content-MD5」和「Content-MD5 是对的」会被混为一谈。
		if want := r.Header.Get("Content-Md5"); want != "" {
			sum := md5.Sum(body)
			if got := base64.StdEncoding.EncodeToString(sum[:]); got != want {
				writeS3Error(w, http.StatusBadRequest, "BadDigest",
					fmt.Sprintf("the Content-MD5 %q does not match the body %q", want, got))
				return
			}
			f.md5Checks++
		}
		f.puts++
		f.objects[key] = fakeS3Entry{body: body, mod: time.Now().UTC().Truncate(time.Second)}
		w.Header().Set("ETag", `"fake-etag"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete:
		// 真 S3 对不存在的 key 也回 204；这里刻意回 404，专门验我们的幂等映射。
		if _, ok := f.objects[key]; !ok {
			writeS3Error(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
	}
}

func (f *fakeS3) writeList(w http.ResponseWriter, bucket, prefix string) {
	if f.listStatus != 0 {
		writeS3Error(w, f.listStatus, f.listCode, "listing refused")
		return
	}
	// 条目必须先攒进缓冲区：S3 的 Contents 是根元素的子节点，边遍历边写响应就把它们
	// 写到了根元素前面，XML 直接非法。
	var contents bytes.Buffer
	count := 0
	appendEntry := func(key string, body []byte) {
		if !strings.HasPrefix(key, prefix) {
			return
		}
		count++
		fmt.Fprintf(&contents, `<Contents><Key>%s</Key><LastModified>2026-10-30T02:00:00.000Z</LastModified>`+
			`<ETag>&quot;fake&quot;</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`,
			key, len(body))
	}
	for key, entry := range f.objects {
		appendEntry(key, entry.body)
	}
	for _, key := range f.extraKeys {
		appendEntry(key, []byte("foreign"))
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-15/">`+
		`<Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys>`+
		`<IsTruncated>false</IsTruncated>%s</ListBucketResult>`, bucket, prefix, count, contents.String())
}

// putCount 是 PUT 次数的加锁读取：断言去重时不能在 handler 之外裸读字段。
func (f *fakeS3) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

func (f *fakeS3) streamingPutCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streamingPuts
}

func (f *fakeS3) multipartRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.multipartRequests
}

func (f *fakeS3) md5CheckCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.md5Checks
}

// fakeCRLF 是 aws-chunked 块分隔符。
var fakeCRLF = []byte("\r\n")

// decodeAWSChunked 还原 minio-go 在明文 HTTP 下给 PUT 体套上的流式签名框架，返回正文与
// 「到达时是否带框架」。
//
// 为什么要解：SDK 的判定是 `streamSha256 && !c.secure`（api.go:971），所以 use_tls=false 时
// PUT 体一定长这样——<hexlen>;chunk-signature=<64hex>\r\n<正文>\r\n 重复若干块（每块上限
// 64KiB），末尾再补一个 0 长度块，真实正文长度放在 x-amz-decoded-content-length。MinIO、
// Ceph RGW 与 AWS 服务端都自己剥这层框架，假桶不剥就会把 172 字节签名头算进对象体积，
// 于是「内容寻址去重」和「List 的 Size」全部对不上。
//
// 这条路径**不是**分片上传：只有单次 PUT，ETag 仍是普通对象的 MD5，§12.2「分片 ETag 形如
// <md5>-N，不能当校验值」那条告诫不受影响。
func decodeAWSChunked(r *http.Request, raw []byte) ([]byte, bool, error) {
	// 判定必须看 X-Amz-Content-Sha256：minio-go 的明文流式签名只设这个标记与
	// x-amz-decoded-content-length，**不写** Content-Encoding: aws-chunked
	// （request-signature-streaming.go:135 只改 req.TransferEncoding，而那里根本不是写头）——
	// MinIO 服务端同样按这个标记决定要不要剥框架，照抄它的判定才不会造出一个只会骗自己的假桶。
	if !strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-AWS4-HMAC-SHA256-PAYLOAD") {
		return raw, false, nil
	}
	var body bytes.Buffer
	for {
		eol := bytes.Index(raw, fakeCRLF)
		if eol < 0 {
			return nil, true, errors.New("aws-chunked: truncated chunk header")
		}
		header := string(raw[:eol])
		sizeText, _, ok := strings.Cut(header, ";")
		if !ok || !strings.Contains(header, "chunk-signature=") {
			return nil, true, fmt.Errorf("aws-chunked: malformed chunk header %q", header)
		}
		size, err := strconv.ParseUint(strings.TrimSpace(sizeText), 16, 64)
		if err != nil {
			return nil, true, fmt.Errorf("aws-chunked: bad chunk size %q: %w", sizeText, err)
		}
		raw = raw[eol+2:]
		if size == 0 {
			break // 末块只携带签名，没有正文
		}
		if uint64(len(raw)) < size+2 {
			return nil, true, errors.New("aws-chunked: truncated chunk data")
		}
		body.Write(raw[:size])
		raw = raw[size+2:]
	}
	// 解码长度必须与声明对得上：对不上说明框架剥错位，后面的每条断言都会变成假通过。
	if want := r.Header.Get("x-amz-decoded-content-length"); want != "" {
		declared, err := strconv.ParseInt(want, 10, 64)
		if err != nil {
			return nil, true, fmt.Errorf("aws-chunked: bad x-amz-decoded-content-length %q: %w", want, err)
		}
		if declared != int64(body.Len()) {
			return nil, true, fmt.Errorf("aws-chunked: decoded %d bytes but header declares %d", body.Len(), declared)
		}
	}
	return body.Bytes(), true, nil
}

func writeS3Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message>`+
		`<Resource>/</Resource><RequestId>fake</RequestId></Error>`, code, xmlEscape(message))
}

// xmlEscape 只处理测试里会出现的这几个字符：Message 里要放带查询串的预签名 URL，
// 不转义 & 会让整段 XML 解析失败，用例就变成「因为解析失败所以看不到敏感串」的假通过。
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

func (f *fakeS3) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.objects)
}

func openFakeS3(t *testing.T, tweak func(*objectstore.S3Config, *fakeS3)) (objectstore.Store, *fakeS3) {
	t.Helper()
	fake, endpoint := newFakeS3(t)
	cfg := s3TestConfig(t)
	cfg.Endpoint = endpoint
	cfg.UseTLS = false
	cfg.CreateBucket = true
	if tweak != nil {
		tweak(&cfg, fake)
	}
	st, err := objectstore.NewS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.(objectstore.Maintainer).Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, fake
}

// TestS3FakeRoundTrip 覆盖写入、内容寻址去重、往返一致、删除与幂等重删。
func TestS3FakeRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, fake := openFakeS3(t, nil)
	spec := objectstore.Spec{Category: objectstore.CategoryReports, Scope: []string{"u-1"}, Ext: ".md"}

	ref, err := st.Put(ctx, spec, strings.NewReader("# report"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if fake.count() != 1 || fake.putCount() != 1 {
		t.Fatalf("写入后桶里有 %d 个对象、发过 %d 次 PUT", fake.count(), fake.putCount())
	}
	// §23.2 不做分片上传的真门禁：一次 Put 只允许一个 PUT、零个 ?uploads。
	if n := fake.multipartRequestCount(); n != 0 {
		t.Errorf("出现了 %d 次 ?uploads 请求，分片上传会让 ETag 变成 <md5>-N", n)
	}
	// 这两条是「探针」而不是「偏好」：明文 HTTP 下 SDK 必然套流式签名框架、必然带上
	// Content-MD5（生产侧显式开了 SendContentMd5）。哪天变成 0，说明依赖升级改了签名路径，
	// 假桶的解码与完整性那一层就得重看——宁可在这里红一次，也不要悄悄失去覆盖。
	if n := fake.streamingPutCount(); n != 1 {
		t.Errorf("带 aws-chunked 框架的 PUT 应为 1 次，got %d（SDK 签名路径变了？）", n)
	}
	if n := fake.md5CheckCount(); n != 1 {
		t.Errorf("假桶比对过 Content-MD5 的次数应为 1，got %d（SendContentMd5 没生效？）", n)
	}
	if ref.UpdatedAt.IsZero() {
		t.Error("Put 应回读一次拿到服务端时间戳")
	}

	// 同内容重复写：必须靠先 Stat 再 PUT 省掉第二次上行流量（§9.1）。
	if _, err := st.Put(ctx, spec, strings.NewReader("# report")); err != nil {
		t.Fatalf("重复 Put: %v", err)
	}
	if fake.putCount() != 1 {
		t.Fatalf("去重失效：PUT 发生了 %d 次", fake.putCount())
	}

	body, got, err := st.Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "# report" || got.SHA256 != ref.SHA256 || got.MediaType != ref.MediaType {
		t.Fatalf("往返不一致: %q ref %+v", content, got)
	}

	if err := st.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Stat(ctx, ref.Key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("删除后 Stat = %v, want ErrNotFound", err)
	}
	// 假桶对不存在的 key 回 404，这里必须仍然成功：GC 重跑要靠它（§9.3）。
	if err := st.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("重复 Delete = %v, want nil", err)
	}
}

// TestS3ListSkipsForeignObjects 钉住「同桶混部时不碰别人的对象」：List 只回调 key 规则
// 拼得出来的条目，否则 §9.3 的孤儿回收会把别的项目的文件当垃圾删掉。
func TestS3ListSkipsForeignObjects(t *testing.T) {
	ctx := context.Background()
	st, _ := openFakeS3(t, nil)
	mine, err := st.Put(ctx, objectstore.Spec{Category: objectstore.CategoryExports, Scope: []string{"u-1"}, Ext: ".csv"}, strings.NewReader("a,b\n"))
	if err != nil {
		t.Fatal(err)
	}

	var listed []objectstore.Ref
	if err := st.List(ctx, objectstore.DefaultPrefix+"/exports/", func(ref objectstore.Ref) error {
		listed = append(listed, ref)
		return nil
	}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List 返回 %d 条，只应该命中自己写的 1 条: %+v", len(listed), listed)
	}
	if listed[0].Key != mine.Key || listed[0].Size != mine.Size || listed[0].MediaType != mine.MediaType {
		t.Fatalf("List 元数据与 Put 不符: %+v vs %+v", listed[0], mine)
	}
}

// TestS3NoSuchBucketIsUnavailable 锁「桶没了」的归类：报成 NotFound 会让对账把全库对象
// 判成 missing（§12）。
func TestS3NoSuchBucketIsUnavailable(t *testing.T) {
	ctx := context.Background()
	st, _ := openFakeS3(t, func(_ *objectstore.S3Config, f *fakeS3) {
		f.listStatus, f.listCode = http.StatusNotFound, "NoSuchBucket"
	})
	err := st.List(ctx, objectstore.DefaultPrefix+"/", func(objectstore.Ref) error { return nil })
	if !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("List = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("桶不存在被报成了对象不存在: %v", err)
	}
}

// TestS3DeniedMessageRedactsPresignedURL 验 §13.3：SDK 错误串常带完整预签名 URL 与凭据，
// 它会被写进日志与审计，必须先脱敏。用例同时断言 AccessDenied 原文可见，避免「XML 没解析
// 成功所以串里本来就没有凭据」这种假通过。
//
// 走 List 而不是 Stat/Get：minio 拿对象元数据发的是 HEAD（Object.Stat() 内部调 StatObject），
// HEAD 的响应体被 Go 丢掉，错误串里只剩「Access Denied.」这种状态码兜底文案，脱敏断言就成了
// 空转；LIST 是带 XML body 的 GET，服务端那句带凭据的 <Message> 才真的进得了错误串。
func TestS3DeniedMessageRedactsPresignedURL(t *testing.T) {
	ctx := context.Background()
	secret := "https://minio.local/k8s-ai-objects/k?X-Amz-Signature=deadbeefcafe1234" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20261030%2Fus-east-1%2Fs3%2Faws4_request" +
		"&AWSAccessKeyId=AKIAIOSFODNN7EXAMPLE"
	st, _ := openFakeS3(t, func(_ *objectstore.S3Config, f *fakeS3) {
		f.denyMessage = "Access Denied by bucket policy while reading " + secret
	})
	key := objectstore.DefaultPrefix + "/reports/u-1/" + strings.Repeat("c", 64) + ".md"
	err := st.List(ctx, key, func(objectstore.Ref) error { return nil })
	if err == nil {
		t.Fatal("403 必须报错")
	}
	if !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("403 = %v, want ErrUnavailable", err)
	}
	message := err.Error()
	if !strings.Contains(message, "Access Denied") {
		t.Fatalf("服务端 Message 没有进入错误串，用例是空转的: %q", message)
	}
	for _, leak := range []string{"deadbeefcafe1234", "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(message, leak) {
			t.Errorf("错误串泄漏凭据 %q: %q", leak, message)
		}
	}
	if !strings.Contains(message, "[REDACTED]") {
		t.Errorf("错误串里没有脱敏标记: %q", message)
	}
}

// TestS3BackendContract 是真机门禁：只有显式给出端点与凭据才跑（方案 15 §21）。
//
// 每个子用例都要一个一次性桶（名字 = 环境变量桶名 + 唯一后缀 + 建桶）：contract 用
// DefaultPrefix 组 key 并断言 List 命中数，共用一个桶会让前一个子用例写的对象污染
// 后一个的计数，和 dir 后端每个子用例各拿一个 t.TempDir() 是同一个理由。
//
// 跑完只清对象不清桶（Store 接口没有 DeleteBucket，为一条测试加接口不值得）：桶名带
// 固定前缀，现场用一条生命周期规则就能收干净。
func TestS3BackendContract(t *testing.T) {
	endpoint := os.Getenv("K8S_AI_TEST_S3_ENDPOINT")
	baseBucket := os.Getenv("K8S_AI_TEST_S3_BUCKET")
	accessKey := os.Getenv("K8S_AI_TEST_S3_ACCESS_KEY")
	secretKey := os.Getenv("K8S_AI_TEST_S3_SECRET_KEY")
	if endpoint == "" || baseBucket == "" || accessKey == "" || secretKey == "" {
		t.Skip("未配置 K8S_AI_TEST_S3_ENDPOINT/BUCKET/ACCESS_KEY/SECRET_KEY，跳过 s3 真机 contract")
	}
	region := envOr("K8S_AI_TEST_S3_REGION", "us-east-1")
	useTLS, err := strconv.ParseBool(envOr("K8S_AI_TEST_S3_USE_TLS", "false"))
	if err != nil {
		t.Fatalf("K8S_AI_TEST_S3_USE_TLS: %v", err)
	}
	pathStyle, err := strconv.ParseBool(envOr("K8S_AI_TEST_S3_PATH_STYLE", "true"))
	if err != nil {
		t.Fatalf("K8S_AI_TEST_S3_PATH_STYLE: %v", err)
	}

	const maxBytes = 1 << 20
	runContract(t, "s3", maxBytes, func(t *testing.T) objectstore.Store {
		t.Helper()
		st, err := objectstore.New(objectstore.Config{
			Driver:       objectstore.DriverS3,
			Endpoint:     endpoint,
			Region:       region,
			UseTLS:       useTLS,
			PathStyle:    pathStyle,
			Bucket:       baseBucket + "-" + contractToken(),
			AccessKey:    accessKey,
			SecretKey:    secretKey,
			CreateBucket: true,
			TmpDir:       t.TempDir(),
			MaxBytes:     maxBytes,
		}, nil)
		if err != nil {
			t.Fatalf("New(s3): %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := st.(objectstore.Maintainer).Ensure(ctx); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		t.Cleanup(func() {
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanCancel()
			_ = st.List(cleanCtx, objectstore.DefaultPrefix+"/", func(ref objectstore.Ref) error {
				_ = st.Delete(cleanCtx, ref.Key)
				return nil
			})
			_ = st.Close()
		})
		return st
	})
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
