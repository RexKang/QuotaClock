package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------- v0.2.4：enabled 回归（schema 层三形态 + 视图输出） ----------

// TestEnabledParseSemantics：PUT 缺 enabled → nil（按启用，兼容旧客户端）；显式 false → 停用。
func TestEnabledParseSemantics(t *testing.T) {
	body := func(prov string) string {
		return `{"version":3,"listen":{"host":"127.0.0.1","port":8787},"collector":{},"auth":{"mode":"admin"},"providers":[` + prov + `]}`
	}
	cases := []struct {
		name    string
		prov    string
		wantNil bool
		wantOn  bool
	}{
		{"缺字段=启用", `{"id":"a","base_url":"https://x.com","paths":["/"]}`, true, true},
		{"显式 false=停用", `{"id":"a","base_url":"https://x.com","paths":["/"],"enabled":false}`, false, false},
		{"显式 true=启用", `{"id":"a","base_url":"https://x.com","paths":["/"],"enabled":true}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			put, err := ParsePut([]byte(body(tc.prov)))
			if err != nil {
				t.Fatal(err)
			}
			p := put.Providers[0]
			if (p.Enabled == nil) != tc.wantNil {
				t.Fatalf("Enabled 指针形态错误: %v", p.Enabled)
			}
			if p.IsEnabled() != tc.wantOn {
				t.Fatalf("IsEnabled = %v, want %v", p.IsEnabled(), tc.wantOn)
			}
		})
	}
}

// TestEnabledFileRoundTrip：nil 落盘不写字段（旧配置零噪音）、false 写明；读回语义一致。
func TestEnabledFileRoundTrip(t *testing.T) {
	mk := func(id string, e *bool) FileProvider {
		return FileProvider{ID: id, Name: id, BaseURL: "https://x.com", Paths: []string{"/"}, Enabled: e}
	}
	f := &File{
		Version: 3, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin, PasswordHash: "h"},
		Providers: []FileProvider{
			mk("on", BoolPtr(true)),
			mk("off", BoolPtr(false)),
			mk("absent", nil),
		},
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"id":"off","name":"off","base_url":"https://x.com","paths":["/"],"enabled":false`) {
		t.Fatalf("停用平台应写明 enabled:false: %s", s)
	}
	if strings.Contains(s, `"id":"absent"`) && strings.Contains(s, `"absent","name":"absent","base_url":"https://x.com","paths":["/"],"enabled":true`) {
		t.Fatalf("nil（缺省启用）不应写 enabled 字段: %s", s)
	}

	back, err := ParseFile(b)
	if err != nil {
		t.Fatalf("落盘形态应可读回: %v", err)
	}
	byID := map[string]FileProvider{}
	for _, p := range back.Providers {
		byID[p.ID] = p
	}
	off, on, absent := byID["off"], byID["on"], byID["absent"]
	if off.IsEnabled() {
		t.Fatal("enabled=false 未保持")
	}
	if !on.IsEnabled() || !absent.IsEnabled() {
		t.Fatalf("true/nil 都应视为启用: %+v", byID)
	}
}

// TestViewEnabledAlwaysEmitted：视图恒输出 enabled（false 不省略），前端据此渲染勾选框与停用标记。
func TestViewEnabledAlwaysEmitted(t *testing.T) {
	f := &File{
		Version: 3, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin},
		Providers: []FileProvider{
			{ID: "on", Name: "ON", BaseURL: "https://x.com", Paths: []string{"/"}},
			{ID: "off", Name: "OFF", BaseURL: "https://y.com", Paths: []string{"/"}, Enabled: BoolPtr(false)},
		},
	}
	for _, authed := range []bool{true, false} {
		v := BuildView(f, nil, authed, false)
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, `"enabled":true`) || !strings.Contains(s, `"enabled":false`) {
			t.Fatalf("视图应恒含 enabled（含未登录）: %s", s)
		}
		if v.Providers[1].Enabled {
			t.Fatalf("停用平台视图应 enabled=false: %+v", v.Providers[1])
		}
	}
}

// TestBuildRuntimeEnabledPointer：BuildRuntime 复制指针（nil 透传），运行期零值不误判为停用。
func TestBuildRuntimeEnabledPointer(t *testing.T) {
	f := &File{
		Version: 3, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin},
		Providers: []FileProvider{
			{ID: "absent", Name: "A", BaseURL: "https://x.com", Paths: []string{"/"}},
			{ID: "off", Name: "B", BaseURL: "https://y.com", Paths: []string{"/"}, Enabled: BoolPtr(false)},
		},
	}
	rt := BuildRuntime(f, nil)
	if !rt.Provider("absent").IsEnabled() {
		t.Fatal("nil 在运行期应视为启用")
	}
	if rt.Provider("off").IsEnabled() {
		t.Fatal("false 在运行期应为停用")
	}
	// 零值 RuntimeProvider（测试/新代码常见构造）默认启用，避免「忘记赋值 = 静默停采」
	var zero RuntimeProvider
	if !zero.IsEnabled() {
		t.Fatal("零值 RuntimeProvider 应默认启用")
	}

	// 指针不共享：改动 File 侧不应影响已构建的 Runtime（CopyBoolPtr）
	f2 := &File{
		Version: 3, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin},
		Providers: []FileProvider{
			{ID: "p", Name: "P", BaseURL: "https://x.com", Paths: []string{"/"}, Enabled: BoolPtr(true)},
		},
	}
	rt2 := BuildRuntime(f2, nil)
	*f2.Providers[0].Enabled = false
	if !rt2.Provider("p").IsEnabled() {
		t.Fatal("Runtime 与 File 不应共享 enabled 指针")
	}
}
