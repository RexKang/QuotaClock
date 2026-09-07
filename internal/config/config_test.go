package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RexKang/QuotaClock/internal/crypto"
)

// validPut 返回一份合法 PUT 基准样本（各规则用例在其上做单项变异）。
func validPut() *Put {
	return &Put{
		Version:   3,
		Listen:    Listen{Host: "127.0.0.1", Port: 8787},
		Collector: DefaultCollector(),
		Auth:      PutAuth{Mode: AuthModeAdmin},
		Providers: []PutProvider{{
			ID: "p1", Name: "P1", BaseURL: "https://api.example.com",
			Paths: []string{"/balance"}, AuthStyle: "bearer",
		}},
	}
}

func fieldOf(errs []ValidationError, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
}

// TestValidateRules C-config-01..12 + 16：R1–R14 逐规则合法/非法样本。
func TestValidateRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Put)
		field  string
		legal  bool
	}{
		{"R1 version", func(p *Put) { p.Version = 2 }, "version", false},
		{"R1 version ok", func(p *Put) { p.Version = 3 }, "version", true},
		{"R2 port low", func(p *Put) { p.Listen.Port = 0 }, "listen.port", false},
		{"R2 port high", func(p *Put) { p.Listen.Port = 65536 }, "listen.port", false},
		{"R2 port ok", func(p *Put) { p.Listen.Port = 65535 }, "listen.port", true},
		{"R3 host bad", func(p *Put) { p.Listen.Host = "localhost" }, "listen.host", false},
		{"R3 host ok", func(p *Put) { p.Listen.Host = "0.0.0.0" }, "listen.host", true},
		{"R4 interval low", func(p *Put) { p.Collector.IntervalBaseS = 29 }, "collector.interval_base_s", false},
		{"R4 interval ok", func(p *Put) { p.Collector.IntervalBaseS = 30 }, "collector.interval_base_s", true},
		{"R5 jitter inv", func(p *Put) { p.Collector.JitterMinS = 10; p.Collector.JitterMaxS = 5 }, "collector.jitter_min_s", false},
		{"R5 jitter ok", func(p *Put) { p.Collector.JitterMinS = 5; p.Collector.JitterMaxS = 25 }, "collector.jitter_min_s", true},
		{"R6 stagger inv", func(p *Put) { p.Collector.StaggerMinS = 6; p.Collector.StaggerMaxS = 5 }, "collector.stagger_min_s", false},
		{"R6 stagger ok", func(p *Put) { p.Collector.StaggerMinS = 1; p.Collector.StaggerMaxS = 5 }, "collector.stagger_min_s", true},
		{"R7 mult low", func(p *Put) { p.Collector.BackoffMultiplier = 0 }, "collector.backoff_multiplier", false},
		{"R7 mult high", func(p *Put) { p.Collector.BackoffMultiplier = 9 }, "collector.backoff_multiplier", false},
		{"R7 mult ok", func(p *Put) { p.Collector.BackoffMultiplier = 8 }, "collector.backoff_multiplier", true},
		{"R8 backoff max high", func(p *Put) { p.Collector.BackoffMaxS = 86401 }, "collector.backoff_max_s", false},
		{"R8 backoff max ok", func(p *Put) { p.Collector.BackoffMaxS = 86400 }, "collector.backoff_max_s", true},
		{"R9 mode bad", func(p *Put) { p.Auth.Mode = "root" }, "auth.mode", false},
		{"R9 mode none", func(p *Put) { p.Auth.Mode = AuthModeNone }, "auth.mode", true},
		{"R10 id empty", func(p *Put) { p.Providers[0].ID = "" }, "providers[0].id", false},
		{"R10 id dup", func(p *Put) {
			p.Providers = append(p.Providers, PutProvider{ID: "p1", BaseURL: "https://x.com", Paths: []string{"/a"}})
		}, "providers[1].id", false},
		{"R11 url bad", func(p *Put) { p.Providers[0].BaseURL = "ftp://x.com" }, "providers[0].base_url", false},
		{"R11 url ok", func(p *Put) { p.Providers[0].BaseURL = "http://127.0.0.1:9999/api" }, "providers[0].base_url", true},
		{"R12 paths empty", func(p *Put) { p.Providers[0].Paths = []string{} }, "providers[0].paths", false},
		{"R12 paths nil", func(p *Put) { p.Providers[0].Paths = nil }, "providers[0].paths", false},
		{"R13 auth_style bad", func(p *Put) { p.Providers[0].AuthStyle = "header" }, "providers[0].auth_style", false},
		{"R13 auth_style cookie", func(p *Put) { p.Providers[0].AuthStyle = "cookie" }, "providers[0].auth_style", true},
		{"R14 header key bad", func(p *Put) { p.Providers[0].ExtraHeaders = map[string]string{"x server": "1"} }, "providers[0].extra_headers", false},
		{"R14 header key ok", func(p *Put) { p.Providers[0].ExtraHeaders = map[string]string{"x-server-id": "abc"} }, "providers[0].extra_headers", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validPut()
			tc.mutate(p)
			errs := Validate(p)
			if tc.legal {
				if len(errs) != 0 {
					t.Fatalf("合法样本被判非法: %v", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatal("非法样本通过校验")
			}
			if !fieldOf(errs, tc.field) {
				t.Fatalf("details 未含 %s: %v", tc.field, errs)
			}
		})
	}
	// 合法基准
	if errs := Validate(validPut()); len(errs) != 0 {
		t.Fatalf("基准样本非法: %v", errs)
	}
	// 缺省 auth_style 视为 bearer（合法）
	p := validPut()
	p.Providers[0].AuthStyle = ""
	if errs := Validate(p); len(errs) != 0 {
		t.Fatalf("缺省 auth_style 应合法: %v", errs)
	}
}

func TestParseUnknownField(t *testing.T) { // C-config-13
	body := `{"version":3,"listenn":{"host":"127.0.0.1","port":8787},"collector":{},"auth":{"mode":"admin"},"providers":[]}`
	if _, err := ParsePut([]byte(body)); err == nil {
		t.Fatal("未知字段未拒绝")
	}
}

func TestParseSensitiveAndComputed(t *testing.T) { // C-config-14
	// 敏感字段 → 400（token_cipher）
	sensitive := `{"version":3,"listen":{"host":"127.0.0.1","port":1},"collector":{},"auth":{"mode":"admin"},"providers":[{"id":"a","base_url":"https://x.com","paths":["/"],"token_cipher":"AAAA"}]}`
	if _, err := ParsePut([]byte(sensitive)); err == nil {
		t.Fatal("password_hash/token_cipher 直写未拒绝")
	} else if !strings.Contains(err.Error(), "token_cipher") {
		t.Fatalf("错误应指出 token_cipher: %v", err)
	}
	hashOnly := `{"version":3,"listen":{"host":"127.0.0.1","port":1},"collector":{},"auth":{"mode":"admin","password_hash":"$2a$10$xyz"},"providers":[]}`
	if _, err := ParsePut([]byte(hashOnly)); err == nil {
		t.Fatal("password_hash 直写未拒绝")
	}
	// 计算字段 → 忽略并通过
	computed := `{
		"version":3,"listen":{"host":"127.0.0.1","port":8787},
		"collector":{"interval_base_s":300,"jitter_min_s":5,"jitter_max_s":25,"stagger_min_s":1,"stagger_max_s":5,"backoff_multiplier":2,"backoff_max_s":1800},
		"auth":{"mode":"admin","password_is_default":true,"authenticated":false},
		"providers":[{"id":"a","name":"A","base_url":"https://x.com","paths":["/b"],"has_token":true,"token_masked":"ab****ef"}]
	}`
	put, err := ParsePut([]byte(computed))
	if err != nil {
		t.Fatalf("计算字段回传被拒: %v", err)
	}
	if errs := Validate(put); len(errs) != 0 {
		t.Fatalf("计算字段剥离后应通过: %v", errs)
	}
}

func TestValidateNoShortCircuit(t *testing.T) { // C-config-15
	p := validPut()
	p.Listen.Port = 70000                                                                                    // R2
	p.Providers[0].Paths = nil                                                                               // R12
	p.Providers = append(p.Providers, PutProvider{ID: "p1", BaseURL: "https://x.com", Paths: []string{"/"}}) // R10 id 重复
	errs := Validate(p)
	if len(errs) < 3 {
		t.Fatalf("应同时返回全部错误项，got %d: %v", len(errs), errs)
	}
	if !fieldOf(errs, "listen.port") || !fieldOf(errs, "providers[0].paths") || !fieldOf(errs, "providers[1].id") {
		t.Fatalf("details 不全: %v", errs)
	}
}

// viewFixture 构造带两个 provider 的 File（一个有 token，一个没有）。
func viewFixture() (*File, map[string]string) {
	cipher, err := sealForTest("abcd1234")
	if err != nil {
		panic(err)
	}
	f := &File{
		Version:   3,
		Listen:    DefaultListen(),
		Collector: DefaultCollector(),
		Auth:      FileAuth{Mode: AuthModeAdmin, PasswordHash: "$2a$10$abcdefghijklmnopqrstuv"},
		Providers: []FileProvider{
			{ID: "a", Name: "A", BaseURL: "https://x.com", Paths: []string{"/a"}, TokenCipher: cipher},
			{ID: "b", Name: "B", BaseURL: "https://y.com", Paths: []string{"/b"}},
		},
	}
	masks := map[string]string{"a": "ab****34"}
	return f, masks
}

func TestViewLoggedIn(t *testing.T) { // C-config-17
	f, masks := viewFixture()
	v := BuildView(f, masks, true, true)
	if !v.Auth.PasswordIsDefault || !v.Auth.Authenticated {
		t.Fatalf("auth 元数据错误: %+v", v.Auth)
	}
	b, _ := json.Marshal(v)
	s := string(b)
	if strings.Contains(s, "token_cipher") || strings.Contains(s, `"token"`) {
		t.Fatal("视图含 token 字段")
	}
	pa := v.Providers[0]
	if !pa.HasToken || pa.TokenMasked != "ab****34" {
		t.Fatalf("登录态应含掩码: %+v", pa)
	}
	if v.Providers[1].HasToken {
		t.Fatal("b 平台不应有 token")
	}
}

func TestViewNotLoggedIn(t *testing.T) { // C-config-18
	f, masks := viewFixture()
	v := BuildView(f, masks, false, true)
	b, _ := json.Marshal(v)
	s := string(b)
	if strings.Contains(s, "token_masked") {
		t.Fatal("未登录不应含 token_masked")
	}
	if !strings.Contains(s, `"has_token":false`) {
		t.Fatal("has_token=false 平台字段应在（无 omitempty）")
	}
	if !v.Auth.PasswordIsDefault {
		t.Fatal("password_is_default 应始终如实返回")
	}
	if v.Auth.Authenticated {
		t.Fatal("未登录 authenticated 应为 false")
	}
}

func TestViewRuneMask(t *testing.T) { // C-config-23
	cipher, _ := sealForTest("中文token🚀")
	f := &File{Version: 3, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin}, Providers: []FileProvider{{ID: "c", BaseURL: "https://z.com", Paths: []string{"/"}, TokenCipher: cipher}}}
	v := BuildView(f, map[string]string{"c": crypto.Mask("中文token🚀")}, true, false)
	// 中文token🚀 = 8 runes：前 2（中文）+ 后 2（n🚀）
	if v.Providers[0].TokenMasked != "中文****n🚀" {
		t.Fatalf("rune 掩码错误: %q", v.Providers[0].TokenMasked)
	}
}

// TestViewEmptyProviders：空配置（新环境模板）providers 序列化为 [] 而非 null，
// 否则前端 addProvBtn 的 cfgView.providers.push 在 null 上崩溃（260906 用户实测）。
func TestViewEmptyProviders(t *testing.T) {
	f := &File{Version: 3, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin}, Providers: []FileProvider{}}
	v := BuildView(f, nil, false, true)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"providers":[]`) {
		t.Fatalf("空配置 providers 应序列化为 [] 而非 null: %s", b)
	}
}

func TestValidateFileParity(t *testing.T) {
	f, _ := viewFixture()
	if errs := ValidateFile(f); len(errs) != 0 {
		t.Fatalf("合法 File 校验失败: %v", errs)
	}
	f.Providers[0].Paths = nil
	errs := ValidateFile(f)
	if !fieldOf(errs, "providers[0].paths") {
		t.Fatalf("File 校验应含 R12: %v", errs)
	}
}
