package objectstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// StreamHash 是给 r 套上「边读边算 sha256 + 边计数 + 超上限立即失败」的读取器。
// 三后端的 Put 共用它：内容寻址要求摘要一定来自实际写入的字节，体积闸门要求超限
// 必须在读到的那一刻就断掉，不能「先读满再判长度」。
type StreamHash struct {
	src  io.Reader
	h    hash.Hash
	max  int64
	size int64
	sum  string
}

// NewStreamHash 包一个读取器。maxBytes <= 0 表示不设上限（只算摘要）。
func NewStreamHash(r io.Reader, maxBytes int64) *StreamHash {
	return &StreamHash{src: r, h: sha256.New(), max: maxBytes}
}

// Read 实现 io.Reader。超过上限返回包装了 ErrTooLarge 的错误。
func (s *StreamHash) Read(p []byte) (int, error) {
	if s.sum != "" {
		return 0, io.EOF
	}
	if s.max > 0 {
		// 只多读一字节用来判定「是否真的还有内容」，避免为了报错把整块缓冲区填满。
		if remaining := s.max - s.size; int64(len(p)) > remaining+1 {
			p = p[:remaining+1]
		}
	}
	n, err := s.src.Read(p)
	if n > 0 {
		s.size += int64(n)
		_, _ = s.h.Write(p[:n])
	}
	if s.max > 0 && s.size > s.max {
		return n, fmt.Errorf("%w: object exceeds limit %d bytes", ErrTooLarge, s.max)
	}
	if err == io.EOF {
		s.sum = hex.EncodeToString(s.h.Sum(nil))
	}
	return n, err
}

// SHA256 返回当前已读字节的摘要；读到 io.EOF 后即为最终摘要。
func (s *StreamHash) SHA256() string {
	if s.sum != "" {
		return s.sum
	}
	return hex.EncodeToString(s.h.Sum(nil))
}

// Size 返回已读字节数。
func (s *StreamHash) Size() int64 { return s.size }

// ReadAllHashed 一次性读完并给出摘要，仅供「正文必须整体驻留」的后端使用（db 后端
// 要把正文写进单行）。dir/s3 必须用 StreamHash 配流式拷贝，不得调它——32MiB 附件
// 全程驻留内存就是这个方案当初否决「调用方自己算摘要」的理由。
func ReadAllHashed(r io.Reader, maxBytes int64) ([]byte, string, error) {
	stream := NewStreamHash(r, maxBytes)
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), stream.SHA256(), nil
}

// mediaTypes 是扩展名到 media type 的确定性映射。
//
// 刻意不用 stdlib 的 mime.TypeByExtension：Windows 上它查注册表，同一份代码在开发机
// 与容器里可能给出不同结果，而 media type 既进 Ref 也进下载响应头，必须跨主机确定。
var mediaTypes = map[string]string{
	".md":   "text/markdown; charset=utf-8",
	".json": "application/json",
	".csv":  "text/csv; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".log":  "text/plain; charset=utf-8",
	".yaml": "application/yaml",
	".yml":  "application/yaml",
	".html": "text/html; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".pdf":  "application/pdf",
	".zip":  "application/zip",
	".gz":   "application/gzip",
	".tar":  "application/x-tar",
}

// OctetStream 是推断不出类型时的回落值。
const OctetStream = "application/octet-stream"

// GuessMediaType 按扩展名推断 media type，未知扩展名回落 OctetStream。
func GuessMediaType(ext string) string {
	if t, ok := mediaTypes[ext]; ok {
		return t
	}
	return OctetStream
}

// ResolveMediaType 按「显式指定 > 扩展名推断 > application/octet-stream」定值。
func ResolveMediaType(mediaType, ext string) string {
	if mediaType != "" {
		return mediaType
	}
	return GuessMediaType(ext)
}
