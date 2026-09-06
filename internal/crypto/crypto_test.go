package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FX-5 会话金样：固定 key 的 HKDF 派生向量（同 key 恒定、不同 key 不同）。
var hkdfGoldenKey = bytes.Repeat([]byte{0x5a}, 32)

func TestSealOpenRoundTrip(t *testing.T) { // C-crypto-01
	key := make([]byte, 32)
	if _, err := randRead(key); err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"", "sk-abc123", "中文token🚀<script>&\"'", strings.Repeat("x", 4096)} {
		sealed, err := SealToken(key, plain)
		if err != nil {
			t.Fatalf("SealToken(%q): %v", plain, err)
		}
		got, err := OpenToken(key, sealed)
		if err != nil {
			t.Fatalf("OpenToken: %v", err)
		}
		if got != plain {
			t.Fatalf("roundtrip mismatch: got %q want %q", got, plain)
		}
	}
}

func TestSealedTamperFails(t *testing.T) { // C-crypto-02
	key := make([]byte, 32)
	_, _ = randRead(key)
	sealed, err := SealToken(key, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(sealed)
	for i := 1; i < len(raw); i++ { // 跳过版本字节，逐位篡改
		bad := append([]byte(nil), raw...)
		bad[i] ^= 0xff
		if _, err := OpenToken(key, base64.RawURLEncoding.EncodeToString(bad)); err == nil {
			t.Fatalf("篡改第 %d 字节后解密竟成功", i)
		}
	}
}

func TestWrongKeyFails(t *testing.T) { // C-crypto-03
	keyA := make([]byte, 32)
	keyB := make([]byte, 32)
	_, _ = randRead(keyA)
	_, _ = randRead(keyB)
	sealed, err := SealToken(keyA, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenToken(keyB, sealed); err == nil {
		t.Fatal("key B 解密 key A 的密文竟成功")
	}
}

func TestSealedFormatGolden(t *testing.T) { // C-crypto-04 密文格式金样：0x01|nonce12|ct+tag16，base64url
	key := bytes.Repeat([]byte{0x11}, 32)
	plain := "abcd" // 4B → 总长 1+12+4+16 = 33
	sealed, err := SealToken(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatalf("非 base64url: %v", err)
	}
	if raw[0] != 0x01 {
		t.Fatalf("版本字节 = %#x，want 0x01", raw[0])
	}
	if len(raw) != 1+12+len(plain)+16 {
		t.Fatalf("密文总长 %d，want %d", len(raw), 1+12+len(plain)+16)
	}
	// 同 key 同明文两次加密：nonce 随机 → 密文不同但均可解
	sealed2, _ := SealToken(key, plain)
	if sealed2 == sealed {
		t.Fatal("两次加密密文相同，nonce 未随机化")
	}
	if _, err := OpenToken(key, sealed2); err != nil {
		t.Fatalf("第二次密文解密失败: %v", err)
	}
}

func TestSessionKeyHKDF(t *testing.T) { // C-crypto-05
	k1 := SessionKey(hkdfGoldenKey)
	k2 := SessionKey(hkdfGoldenKey)
	if !bytes.Equal(k1, k2) {
		t.Fatal("同 key 派生结果不恒定")
	}
	if len(k1) != 32 {
		t.Fatalf("派生长度 %d，want 32", len(k1))
	}
	// 金样向量（RFC5869 语义：HKDF-SHA256(ikm, salt=nil, info="session-cookie")），
	// 用独立实现交叉验证一次：
	if got, want := hexOf(k1), hkdfGoldenVector(); got != want {
		t.Fatalf("HKDF 金样不匹配:\n got %s\nwant %s", got, want)
	}
	other := SessionKey(bytes.Repeat([]byte{0x5b}, 32))
	if bytes.Equal(k1, other) {
		t.Fatal("不同 key 派生结果相同")
	}
}

// hkdfGoldenVector 用两段式（Extract+Expand）手算同一结果交叉验证。
func hkdfGoldenVector() string {
	extractor := hmacSHA256(nil, hkdfGoldenKey) // salt=nil → 全零 key
	info := []byte("session-cookie")
	var okm, prev []byte
	for i := byte(1); len(okm) < 32; i++ {
		prev = hmacSHA256(extractor, append(append([]byte{}, prev...), append(info, i)...))
		okm = append(okm, prev...)
	}
	return hexOf(okm[:32])
}

func TestEnsureKeyGenerateAndLoad(t *testing.T) { // C-crypto-06
	dir := t.TempDir()
	key1, err := EnsureKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(key1) != 32 {
		t.Fatalf("key 长度 %d", len(key1))
	}
	// 持久化 + 复载一致
	key2, err := EnsureKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("两次加载 key 不一致")
	}
	if runtimeOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "key.bin"))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key.bin 权限 = %v，want 0600", fi.Mode().Perm())
		}
	}
}

func TestEnsureKeyCorrupt(t *testing.T) { // §6.1 长度≠32 → 报错
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key.bin"), make([]byte, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureKey(dir); err == nil {
		t.Fatal("损坏 key.bin 未报错")
	}
}

func TestMask(t *testing.T) { // C-crypto-07
	cases := []struct{ in, want string }{
		{"abcd1234", "ab****34"},
		{"abc", "****"},
		{"", "****"},
		{"abcd", "ab****cd"},
		{"中文token🚀xyz", "中文****yz"}, // rune 化：中文/emoji 不乱码
	}
	for _, c := range cases {
		if got := Mask(c.in); got != c.want {
			t.Errorf("Mask(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskRuneEdge(t *testing.T) { // C-config-23 / C-crypto 补充：多字节 rune 完整性
	got := Mask("中文字符")
	if got != "中文****字符" {
		t.Fatalf("Mask(中文字符) = %q", got)
	}
}

func TestDecryptFailNoPanic(t *testing.T) { // C-crypto-08
	key := make([]byte, 32)
	_, _ = randRead(key)
	if _, err := OpenToken(key, "not-a-valid-cipher!!"); err == nil {
		t.Fatal("非法密文解密未报错")
	}
	// 空、截断、错版本字节都不 panic
	for _, bad := range []string{"", "AAAA", "AAA"} {
		if _, err := OpenToken(key, bad); err == nil {
			t.Fatalf("密文 %q 解密未报错", bad)
		}
	}
}

func TestSessionSignVerifyRoundTrip(t *testing.T) { // C-srv-04 前置
	key := SessionKey(hkdfGoldenKey)
	val, err := SignSession(key, SessionClaims{SID: "abcdef0123456789abcdef0123456789", Exp: timeNow().Add(3600e9).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifySession(key, val)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.SID != "abcdef0123456789abcdef0123456789" {
		t.Fatalf("sid = %q", claims.SID)
	}
	// 篡改 payload 1 字节 → SESSION_INVALID
	raw, _ := base64.RawURLEncoding.DecodeString(val[:strings.IndexByte(val, '.')])
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["exp"] = any(m["exp"].(float64) + 1)
	tampered, _ := json.Marshal(m)
	dot := strings.IndexByte(val, '.')
	bad := base64.RawURLEncoding.EncodeToString(tampered) + val[dot:]
	if _, err := VerifySession(key, bad); err != ErrSessionInvalid {
		t.Fatalf("篡改载荷 verify err = %v, want ErrSessionInvalid", err)
	}
}
