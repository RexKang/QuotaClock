// Package crypto 承载 QuotaClock 的密钥与加密：key.bin 管理、AES-256-GCM token 加解密、
// HKDF 会话签名密钥派生、token 掩码。
package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeyLen 是 master key 固定长度（AES-256）。
const KeyLen = 32

// EnsureKey 返回配置目录下 key.bin 的 32 字节主密钥；不存在则用 crypto/rand 生成并写入（POSIX 0600）。
// 加载失败（损坏 / 长度≠32）返回错误——调用方应退出并提示「删除后需重录全部 token」。
func EnsureKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, "key.bin")
	if data, err := os.ReadFile(path); err == nil {
		if len(data) != KeyLen {
			return nil, fmt.Errorf("密钥文件损坏（长度 %d ≠ %d 字节）: %s；删除该文件后需重新录入全部 token", len(data), KeyLen, path)
		}
		return data, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取密钥文件失败: %s: %w", path, err)
	}
	key := make([]byte, KeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成随机密钥失败: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("写入密钥文件失败: %s: %w", path, err)
	}
	return key, nil
}
