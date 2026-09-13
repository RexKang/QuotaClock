package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// defaultCollectorJSON 校验通过的最小 collector 段（R4 要求 interval ≥30、multiplier ∈[1,8]）。
const defaultCollectorJSON = `"collector":{"interval_base_s":300,"jitter_min_s":5,"jitter_max_s":25,` +
	`"stagger_min_s":1,"stagger_max_s":5,"backoff_multiplier":2,"backoff_max_s":1800}`

// TestBalanceDefaultAndNormalize：零值/缺字段回落默认，显式配置不动。
func TestBalanceDefaultAndNormalize(t *testing.T) {
	if got := NormalizeBalance(Balance{}); got != DefaultBalance() {
		t.Fatalf("零值应回落默认: %+v", got)
	}
	if got := NormalizeBalance(Balance{Full: -1}); got != DefaultBalance() {
		t.Fatalf("负数满额应回落默认: %+v", got)
	}
	custom := Balance{Full: 200, GreenPct: 60, WarnPct: 30}
	if got := NormalizeBalance(custom); got != custom {
		t.Fatalf("显式配置不应被改写: %+v", got)
	}
	if DefaultBalance() != (Balance{Full: 100, GreenPct: 50, WarnPct: 20}) {
		t.Fatalf("默认值应为 满额 100 / 绿 50 / 黄 20: %+v", DefaultBalance())
	}
}

// TestValidateBalance：R13 规则。
func TestValidateBalance(t *testing.T) {
	cases := []struct {
		name  string
		b     Balance
		ok    bool
		field string
	}{
		{"默认", DefaultBalance(), true, ""},
		{"未设置（0）不报错", Balance{}, true, ""},
		{"绿超范围", Balance{Full: 100, GreenPct: 101, WarnPct: 20}, false, "balance.green_pct"},
		{"黄超范围", Balance{Full: 100, GreenPct: 50, WarnPct: 120}, false, "balance.warn_pct"},
		{"黄高于绿", Balance{Full: 100, GreenPct: 20, WarnPct: 50}, false, "balance.warn_pct"},
		{"黄等于绿（两档）", Balance{Full: 100, GreenPct: 50, WarnPct: 50}, true, ""},
		{"满额过大", Balance{Full: 1e13, GreenPct: 50, WarnPct: 20}, false, "balance.full"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateBalance(tc.b)
			if tc.ok && len(errs) > 0 {
				t.Fatalf("应通过，却报: %+v", errs)
			}
			if !tc.ok {
				if len(errs) == 0 {
					t.Fatal("应报错")
				}
				if errs[0].Field != tc.field {
					t.Fatalf("字段应为 %s，实际 %s", tc.field, errs[0].Field)
				}
			}
		})
	}
}

// TestPutBalanceRoundTrip：PUT 解析带 balance，回传形态被 Validate 接受；R13 同时作用在 PUT 与文件两路。
func TestPutBalanceRoundTrip(t *testing.T) {
	body := `{"version":5,"listen":{"host":"127.0.0.1","port":8787},` + defaultCollectorJSON + `,"balance":{"full":200,"green_pct":60,"warn_pct":30},
	  "auth":{"mode":"admin"},"providers":[{"platform":"deepseek","access_keys":[{"id":"k1","token":"t"}]}]}`
	p, err := ParsePut([]byte(body))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.Balance != (Balance{Full: 200, GreenPct: 60, WarnPct: 30}) {
		t.Fatalf("balance 未解析: %+v", p.Balance)
	}
	if errs := Validate(p); len(errs) > 0 {
		t.Fatalf("应通过: %+v", errs)
	}

	// 缺 balance 段（旧客户端 / 手写文件）不报错，由 NormalizeBalance 兜默认
	noBal := `{"version":5,"listen":{"host":"127.0.0.1","port":8787},` + defaultCollectorJSON + `,"auth":{"mode":"admin"},
	  "providers":[{"platform":"deepseek","access_keys":[{"id":"k1","token":"t"}]}]}`
	p2, err := ParsePut([]byte(noBal))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if errs := Validate(p2); len(errs) > 0 {
		t.Fatalf("缺 balance 应通过（回落默认）: %+v", errs)
	}
	if got := NormalizeBalance(p2.Balance); got != DefaultBalance() {
		t.Fatalf("应回落默认: %+v", got)
	}

	// R13 违例经 PUT 通道返回
	bad := `{"version":5,"listen":{"host":"127.0.0.1","port":8787},` + defaultCollectorJSON + `,"balance":{"full":100,"green_pct":10,"warn_pct":80},
	  "auth":{"mode":"admin"},"providers":[{"platform":"deepseek","access_keys":[{"id":"k1","token":"t"}]}]}`
	p3, err := ParsePut([]byte(bad))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	errs := Validate(p3)
	if len(errs) == 0 || !strings.Contains(errs[0].Field, "balance") {
		t.Fatalf("应报 balance 违例: %+v", errs)
	}
}

// TestBalanceInView：GET 视图带 balance 且补默认；落盘 JSON 含 balance 段。
func TestBalanceInView(t *testing.T) {
	f := &File{Version: CurrentVersion, Listen: Listen{Host: "127.0.0.1", Port: 8787},
		Auth: FileAuth{Mode: AuthModeAdmin}, Providers: []FileProvider{}}
	v := BuildView(f, map[string]string{}, false, false)
	if v.Balance != DefaultBalance() {
		t.Fatalf("视图应补默认 balance: %+v", v.Balance)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"balance"`) {
		t.Fatalf("落盘应含 balance 段: %s", raw)
	}
}

// TestRuntimeCarriesBalance：热生效时 balance 随配置进 Runtime。
func TestRuntimeCarriesBalance(t *testing.T) {
	f := &File{Version: CurrentVersion, Collector: DefaultCollector(), Auth: FileAuth{Mode: AuthModeAdmin},
		Balance: Balance{Full: 80, GreenPct: 40, WarnPct: 10}, Providers: []FileProvider{}}
	if r := BuildRuntime(f, []byte("0123456789abcdef0123456789abcdef")); r.Balance != f.Balance {
		t.Fatalf("Runtime 未带上 balance: %+v", r.Balance)
	}
}
