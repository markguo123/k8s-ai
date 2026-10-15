package cluster

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// envCipherKey 是平台主进程读取凭据加密密钥的环境变量（P6.12：敏感值只从环境变量读取）。
const envCipherKey = "K8S_AI_ENC_KEY"

// cipherPrefix 标识当前密文格式版本。
const cipherPrefix = "v1:"

// ErrCipherKeyRequired 表示未配置凭据加密密钥。
var ErrCipherKeyRequired = errors.New("cluster: encryption key is required (set K8S_AI_ENC_KEY to a 32-byte hex or base64 key)")

// Cipher 是凭据的 AES-256-GCM 加密/解密器（P3.1：kubeconfig 加密落库）。
// 密钥永不落库、永不写日志；解密后的明文在调用方使用完毕后即丢弃。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipherFromEnv 从环境变量构造密码机。密钥接受 64 位 hex 或 base64 编码的
// 32 字节随机密钥；格式非法时返回错误，避免弱口令退化。
func NewCipherFromEnv() (*Cipher, error) {
	raw := strings.TrimSpace(os.Getenv(envCipherKey))
	if raw == "" {
		return nil, ErrCipherKeyRequired
	}
	key, err := decodeCipherKey(raw)
	if err != nil {
		return nil, err
	}
	return NewCipher(key)
}

// NewCipher 用给定 32 字节密钥构造 AES-256-GCM 密码机。
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("cluster: cipher key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cluster: create aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cluster: create gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt 加密明文并返回带版本前缀的 base64 密文。
func (c *Cipher) Encrypt(plain []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("cluster: generate nonce: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, plain, nil)
	blob := make([]byte, 0, len(nonce)+len(sealed))
	blob = append(blob, nonce...)
	blob = append(blob, sealed...)
	return cipherPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt 解密 Encrypt 的输出；格式或完整性校验失败时明确报错。
func (c *Cipher) Decrypt(encoded string) ([]byte, error) {
	if !strings.HasPrefix(encoded, cipherPrefix) {
		return nil, errors.New("cluster: unsupported ciphertext format")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, cipherPrefix))
	if err != nil {
		return nil, fmt.Errorf("cluster: decode ciphertext: %w", err)
	}
	nonceSize := c.aead.NonceSize()
	if len(raw) < nonceSize+16 {
		return nil, errors.New("cluster: ciphertext too short")
	}
	nonce := raw[:nonceSize]
	ct := raw[nonceSize:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("cluster: decrypt failed: %w", err)
	}
	return plain, nil
}

func decodeCipherKey(raw string) ([]byte, error) {
	if len(raw) == 64 {
		if key, err := hex.DecodeString(raw); err == nil {
			return key, nil
		}
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	return nil, fmt.Errorf("cluster: invalid cipher key encoding (want 32-byte hex or base64)")
}
