package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/hkdf"
)

// sessionInfo 是 HKDF 的 info 参数（PRD §4.3：会话密钥派生固定 info="session-cookie"）。
const sessionInfo = "session-cookie"

// SessionKey 从 master key 派生会话 cookie 签名密钥：
// HKDF-SHA256(ikm=key.bin, salt=nil, info="session-cookie", L=32)。
func SessionKey(master []byte) []byte {
	out := make([]byte, 32)
	// hkdf 固定长度读取恒成功；错误仅在 ikm 为空等异常场景出现
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte(sessionInfo)), out); err != nil {
		panic("hkdf derive: " + err.Error())
	}
	return out
}

// SessionTTL 管理员会话有效期（PRD §6.2：7 天）。
const SessionTTL = 7 * 24 * time.Hour

// SessionClaims 是签名载荷（无敏感值，D-4：签名不加密）。
type SessionClaims struct {
	SID string `json:"sid"` // 16 字节 hex
	Exp int64  `json:"exp"` // unix 秒
}

var (
	ErrSessionFormat  = errors.New("session cookie 格式非法")
	ErrSessionInvalid = errors.New("session 签名无效或已过期")
)

// SignSession 生成 cookie 值：base64url(payload) + "." + base64url(HMAC-SHA256(key, payload))。
func SignSession(key []byte, claims SessionClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifySession 校验 cookie 值并还原载荷；签名无效/已过期返回 ErrSessionInvalid，格式坏返回 ErrSessionFormat。
func VerifySession(key []byte, value string) (SessionClaims, error) {
	var claims SessionClaims
	dot := -1
	for i := 0; i < len(value); i++ {
		if value[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return claims, ErrSessionFormat
	}
	payload, err := base64.RawURLEncoding.DecodeString(value[:dot])
	if err != nil {
		return claims, ErrSessionFormat
	}
	sig, err := base64.RawURLEncoding.DecodeString(value[dot+1:])
	if err != nil {
		return claims, ErrSessionFormat
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return claims, ErrSessionInvalid
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, ErrSessionFormat
	}
	if claims.Exp <= time.Now().Unix() {
		return claims, ErrSessionInvalid
	}
	return claims, nil
}

// NewSID 生成 16 字节 hex 会话 ID。
func NewSID() (string, error) {
	b := make([]byte, 16)
	if _, err := randRead(b); err != nil {
		return "", fmt.Errorf("生成 sid 失败: %w", err)
	}
	return hexEncode(b), nil
}
