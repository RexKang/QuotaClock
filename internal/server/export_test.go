package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestExportEndpoint（v0.3.0）：导出配置的权限、响应头与内容。
// 要点：含明文 token（跨机器搬运的唯一可行形态）、不含密码 hash、
// 未登录被拒（与 PUT/TEST 同一守卫）、no-store、文件名带日期。
func TestExportEndpoint(t *testing.T) {
	env := newEnv(t, nil)

	// 未登录：admin 模式下必须 401
	resp := env.do("GET", "/api/config/export", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	cookie := env.login("Quota@2026090S")

	// 先写入一份带 token 的配置
	put := validPutBody(env)
	put["providers"] = []any{map[string]any{
		"platform": "deepseek",
		"access_keys": []any{
			map[string]any{"id": "k1", "name": "主", "token": "sk-export-me"},
		},
	}}
	code, body, _ := env.doJSON("PUT", "/api/config", put, authHdr(cookie))
	if code != 200 {
		t.Fatalf("准备配置失败: %d %v", code, body)
	}

	// 导出
	resp = env.do("GET", "/api/config/export", nil, authHdr(cookie))
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("导出应 200，实际 %d：%s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type 不对: %s", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "quotaclock-config-") {
		t.Fatalf("Content-Disposition 不对: %s", cd)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("应 no-store，实际 %q", cc)
	}

	s := string(raw)
	if !strings.Contains(s, "sk-export-me") {
		t.Fatalf("导出应含明文 token：%s", s)
	}
	for _, bad := range []string{"password_hash", "token_cipher", "password\""} {
		if strings.Contains(s, bad) {
			t.Fatalf("导出泄露 %s：%s", bad, s)
		}
	}
	var ex map[string]any
	if err := json.Unmarshal(raw, &ex); err != nil {
		t.Fatalf("导出不是合法 JSON: %v", err)
	}
	if ex["_note"] == nil || ex["balance"] == nil {
		t.Fatalf("导出应含 _note 与 balance: %v", ex)
	}
	if v, ok := ex["version"].(float64); !ok || v != 5 {
		t.Fatalf("导出应为当前 schema 版本 5: %v", ex["version"])
	}

	// 同一时刻的脱敏视图仍必须是掩码（导出不影响 GET 的脱敏语义）
	_, view, _ := env.doJSON("GET", "/api/config", nil, authHdr(cookie))
	vs := mustJSON(view)
	if strings.Contains(vs, "sk-export-me") {
		t.Fatalf("GET /api/config 不该出现明文 token: %s", vs)
	}

	// 日志留痕：导出意味着明文出过机
	if !strings.Contains(env.logBuf.String(), "已导出配置") {
		t.Fatalf("应有导出 INFO 日志: %s", env.logBuf.String())
	}
}
