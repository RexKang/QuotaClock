package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// cipherVersionByte 预留算法迁移：0x01 = AES-256-GCM（设计 D-3）。
const cipherVersionByte = 0x01

const (
	nonceLen = 12
	tagLen   = 16
)

// SealToken 把明文 token 加密为 base64url( 0x01 | nonce(12B) | ciphertext+tag )。
func SealToken(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := make([]byte, 0, 1+nonceLen+len(plaintext)+tagLen)
	out = append(out, cipherVersionByte)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// OpenToken 解密 SealToken 的产物；认证失败（篡改/密钥不匹配）返回错误。
func OpenToken(key []byte, sealed string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("token 密文 base64 解码失败: %w", err)
	}
	if len(raw) < 1+nonceLen+tagLen {
		return "", errors.New("token 密文长度非法")
	}
	if raw[0] != cipherVersionByte {
		return "", fmt.Errorf("token 密文算法版本不支持: 0x%02x", raw[0])
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, raw[1:1+nonceLen], raw[1+nonceLen:], nil)
	if err != nil {
		return "", fmt.Errorf("token 解密失败: %w", err)
	}
	return string(plain), nil
}
