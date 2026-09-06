package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"time"
)

// 测试辅助（仅测试文件引用）。

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func timeNow() time.Time { return time.Now() }

const runtimeOS = runtime.GOOS
