package cluster

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	ci, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	plain := []byte("apiVersion: v1\nkind: Config\nclusters:\n- name: c1\n")
	enc, err := ci.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if strings.Contains(enc, string(plain[:16])) {
		t.Fatal("密文不应包含明文片段")
	}
	if !strings.HasPrefix(enc, cipherPrefix) {
		t.Fatalf("密文缺版本前缀: %q", enc)
	}
	got, err := ci.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("解密结果不一致: %q != %q", got, plain)
	}
}

func TestCipherRandomNonce(t *testing.T) {
	ci, err := NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := ci.Encrypt([]byte("same"))
	b, _ := ci.Encrypt([]byte("same"))
	if a == b {
		t.Fatal("两次加密不应相同（随机 nonce）")
	}
}

func TestCipherTamperDetected(t *testing.T) {
	ci, err := NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := ci.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ci.Decrypt(enc[:len(enc)-2] + "AA"); err == nil {
		t.Fatal("篡改后的密文应解密失败")
	}
	if _, err := ci.Decrypt("v2:" + enc); err == nil {
		t.Fatal("未知版本前缀应报错")
	}
	if _, err := ci.Decrypt("garbage"); err == nil {
		t.Fatal("非法密文格式应报错")
	}
}

func TestNewCipherKeyValidation(t *testing.T) {
	if _, err := NewCipher([]byte("short")); err == nil {
		t.Fatal("非 32 字节密钥应报错")
	}
	key := make([]byte, 32)
	if _, err := NewCipher(key); err != nil {
		t.Fatalf("32 字节密钥应可用: %v", err)
	}
}

func TestDecodeCipherKey(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	hexKey := hex.EncodeToString(raw)
	got, err := decodeCipherKey(hexKey)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("hex 密钥解码失败: %v", err)
	}
	b64Key := base64.StdEncoding.EncodeToString(raw)
	got, err = decodeCipherKey(b64Key)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("base64 密钥解码失败: %v", err)
	}
	if _, err := decodeCipherKey("not-a-key"); err == nil {
		t.Fatal("非法密钥编码应报错")
	}
}
