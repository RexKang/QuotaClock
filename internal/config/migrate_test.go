package config

import (
	"strings"
	"testing"

	"github.com/RexKang/QuotaClock/internal/crypto"
)

// FX-2：v0.1 导出 JSON（version 2）脱敏版——结构与真实 old-config.json 一致，token 为占位。
const fx2OldConfig = `{
  "version": 2,
  "active": "deepseek",
  "providers": [
    {
      "enabled": true,
      "id": "zhipu",
      "name": "智谱 GLM",
      "baseURL": "https://open.bigmodel.cn",
      "token": "eyJhbGciOiJQUkFDSElORV9GSVhUVVJFLXRva2VuIn0.test",
      "endpoints": [
        { "method": "GET", "path": "/api/monitor/usage/quota/limit", "params": "{", "note": "周期额度" }
      ]
    },
    {
      "enabled": true,
      "id": "deepseek",
      "name": "DeepSeek",
      "baseURL": "https://api.deepseek.com",
      "token": "sk-test-placeholder",
      "endpoints": [
        { "method": "GET", "path": "/user/balance", "params": "", "note": "余额" }
      ]
    },
    {
      "enabled": true,
      "id": "moonshot",
      "name": "Kimi Code",
      "baseURL": "http://127.0.0.1:8787/kimi",
      "token": "sk-kimi-placeholder",
      "endpoints": [
        { "method": "GET", "path": "/usages", "params": "", "note": "周期额度" },
        { "method": "GET", "path": "/me", "params": "", "note": "账户信息" },
        { "method": "GET", "path": "/models", "params": "", "note": "模型列表" }
      ]
    },
    {
      "enabled": true,
      "id": "opencode",
      "name": "OpenCode Go",
      "baseURL": "http://127.0.0.1:8787/opencode",
      "token": "auth=Fe26.2**placeholder; oc_locale=zh",
      "endpoints": [
        { "method": "GET", "path": "/_server?id=deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef&args=%7B%22t%22%3A9%7D", "params": "", "note": "Go 套餐用量" }
      ]
    },
    {
      "enabled": false,
      "id": "disabledprov",
      "name": "已禁用平台",
      "baseURL": "https://disabled.example.com",
      "token": "sk-disabled",
      "endpoints": [ { "method": "GET", "path": "/x" } ]
    }
  ]
}`

func sealReal(plain string) (string, error) {
	key := bytes32()
	return crypto.SealToken(key, plain)
}

func bytes32() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func TestMigrateV1PlanA(t *testing.T) { // C-config-21
	out, logs, err := MigrateV1([]byte(fx2OldConfig), sealReal)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(logs, "\n")
	if out.Version != 3 {
		t.Fatalf("version = %d", out.Version)
	}
	if out.Listen != DefaultListen() || out.Collector != DefaultCollector() {
		t.Fatal("listen/collector 应为默认值")
	}
	if out.Auth.Mode != AuthModeAdmin {
		t.Fatal("auth 应为 admin")
	}
	if !IsDefaultPassword(out.Auth.PasswordHash) {
		t.Fatal("迁移后应为默认密码态")
	}

	byID := map[string]FileProvider{}
	for _, p := range out.Providers {
		byID[p.ID] = p
	}
	if len(out.Providers) != 5 {
		t.Fatalf("应导入 5 个（含 enabled=false 导入并停用），got %d", len(out.Providers))
	}
	dp, ok := byID["disabledprov"]
	if !ok {
		t.Fatal("enabled=false 应导入并保持停用")
	}
	if dp.IsEnabled() {
		t.Fatal("disabledprov 应保持停用（enabled=false）")
	}
	if dp.TokenCipher == "" {
		t.Fatal("停用平台 token 仍应加密落盘（不丢配置）")
	}
	zp := byID["zhipu"]
	if !zp.IsEnabled() {
		t.Fatal("enabled=true 平台应启用")
	}
	if !strings.Contains(joined, "enabled=false") || !strings.Contains(joined, "保持停用") {
		t.Fatalf("应记录 enabled=false 停用导入日志: %s", joined)
	}

	// zhipu：baseURL→base_url，params="{" 不告警，paths 序保持
	z := byID["zhipu"]
	if z.BaseURL != "https://open.bigmodel.cn" {
		t.Fatalf("zhipu base_url = %q", z.BaseURL)
	}
	if len(z.Paths) != 1 || z.Paths[0] != "/api/monitor/usage/quota/limit" {
		t.Fatalf("zhipu paths = %v", z.Paths)
	}
	if z.TokenCipher == "" {
		t.Fatal("zhipu token 应加密落盘")
	}
	// 加密落盘可解（迁移映射正确性）
	plain, err := crypto.OpenToken(bytes32(), z.TokenCipher)
	if err != nil || plain != "eyJhbGciOiJQUkFDSElORV9GSVhUVVJFLXRva2VuIn0.test" {
		t.Fatalf("zhipu token 密文不可解或值不符: %q %v", plain, err)
	}

	// kimi：自动改写 → https://api.kimi.com/coding/v1，3 条 path 保持
	k := byID["moonshot"]
	if k.BaseURL != "https://api.kimi.com/coding/v1" {
		t.Fatalf("kimi base_url = %q", k.BaseURL)
	}
	if len(k.Paths) != 3 || k.Paths[0] != "/usages" || k.Paths[1] != "/me" || k.Paths[2] != "/models" {
		t.Fatalf("kimi paths = %v", k.Paths)
	}
	if k.AuthStyle != "" { // kimi 不改 auth_style（缺省 bearer）
		t.Fatalf("kimi auth_style = %q", k.AuthStyle)
	}
	if !strings.Contains(joined, "https://api.kimi.com/coding/v1") {
		t.Fatalf("应 INFO 记录改写对照: %s", joined)
	}

	// opencode：改写为官方用量接口（260907）；旧 Cookie token 无法转 API Key，不迁移 + WARN
	o := byID["opencode"]
	if o.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("opencode base_url = %q", o.BaseURL)
	}
	if len(o.Paths) != 1 || o.Paths[0] != "/usage" {
		t.Fatalf("opencode paths = %v", o.Paths)
	}
	if o.AuthStyle != "" {
		t.Fatalf("opencode auth_style = %q（应为缺省 bearer）", o.AuthStyle)
	}
	if o.TokenCipher != "" {
		t.Fatal("opencode 旧 Cookie token 不应迁移（无法转 API Key）")
	}
	if !strings.Contains(joined, "无法转换为 API Key") {
		t.Fatalf("应 WARN token 未迁移提示: %s", joined)
	}
	if !strings.Contains(joined, "/zen/go/v1") {
		t.Fatalf("应 INFO 记录改写对照: %s", joined)
	}
}

func TestMigrateV1Fallback(t *testing.T) { // C-config-22 兜底：非 8787 自定义代理 → 跳过 + WARN，其余照常
	fallback := `{
      "version": 2,
      "providers": [
        {
          "enabled": true, "id": "custom", "name": "自定义代理",
          "baseURL": "http://127.0.0.1:9000/mykimi", "token": "sk-x",
          "endpoints": [ { "method": "GET", "path": "/usages" } ]
        },
        {
          "enabled": true, "id": "deepseek", "name": "DeepSeek",
          "baseURL": "https://api.deepseek.com", "token": "sk-y",
          "endpoints": [ { "method": "GET", "path": "/user/balance" } ]
        }
      ]
    }`
	out, logs, err := MigrateV1([]byte(fallback), sealReal)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Providers) != 1 || out.Providers[0].ID != "deepseek" {
		t.Fatalf("兜底应只保留 deepseek: %+v", out.Providers)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "127.0.0.1:9000") || !strings.Contains(joined, "README") {
		t.Fatalf("兜底 WARN 应含原地址与 README 指引: %s", joined)
	}
}

func TestMigrateV1NonGETWarn(t *testing.T) {
	src := `{"version":2,"providers":[{"id":"a","name":"A","baseURL":"https://x.com","token":"t",
	  "endpoints":[{"method":"POST","path":"/go"},{"method":"GET","path":"/ok"}]}]}`
	out, logs, err := MigrateV1([]byte(src), sealReal)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Providers[0].Paths) != 1 || out.Providers[0].Paths[0] != "/ok" {
		t.Fatalf("非 GET 应丢弃: %v", out.Providers[0].Paths)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "POST") {
		t.Fatal("非 GET 丢弃应 WARN")
	}
}

func TestMigrateV1BadJSON(t *testing.T) {
	if _, _, err := MigrateV1([]byte("{broken"), sealReal); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}
