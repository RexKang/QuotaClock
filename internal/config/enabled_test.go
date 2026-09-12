package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------- v0.2.4/v0.2.5：enabled（凭据级）三形态 + 视图输出 ----------

// TestEnabledParseSemantics：PUT 缺 enabled → nil（按启用，兼容旧客户端）；显式 false → 停用。
func TestEnabledParseSemantics(t *testing.T) {
	body := func(key string) string {
		return `{"version":4,"listen":{"host":"127.0.0.1","port":8787},"collector":{},"auth":{"mode":"admin"},"providers":[{"platform":"deepseek","access_keys":[` + key + `]}]}`
	}
	cases := []struct {
		name    string
		key     string
		wantNil bool
		wantOn  bool
	}{
		{"缺字段=启用", `{"id":"k1","name":"A1"}`, true, true},
		{"显式 false=停用", `{"id":"k1","name":"A1","enabled":false}`, false, false},
		{"显式 true=启用", `{"id":"k1","name":"A1","enabled":true}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			put, err := ParsePut([]byte(body(tc.key)))
			if err != nil {
				t.Fatal(err)
			}
			k := put.Providers[0].AccessKeys[0]
			if (k.Enabled == nil) != tc.wantNil {
				t.Fatalf("Enabled 指针形态错误: %v", k.Enabled)
			}
			if k.IsEnabled() != tc.wantOn {
				t.Fatalf("IsEnabled = %v, want %v", k.IsEnabled(), tc.wantOn)
			}
		})
	}
}

// TestEnabledFileRoundTrip：nil 落盘不写字段（旧配置零噪音）、false 写明；读回语义一致。
func TestEnabledFileRoundTrip(t *testing.T) {
	f := &File{
		Version: CurrentVersion, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin, PasswordHash: "h"},
		Providers: []FileProvider{{Platform: "opencode", AccessKeys: []AccessKey{
			{ID: "on", Name: "ON", Enabled: BoolPtr(true)},
			{ID: "off", Name: "OFF", Enabled: BoolPtr(false)},
			{ID: "absent", Name: "ABSENT"},
		}}},
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `{"id":"off","name":"OFF","enabled":false}`) {
		t.Fatalf("停用凭据应写明 enabled:false: %s", s)
	}
	if strings.Contains(s, `"id":"absent","name":"ABSENT","enabled"`) {
		t.Fatalf("nil（缺省启用）不应写 enabled 字段: %s", s)
	}

	back, err := ParseFile(b)
	if err != nil {
		t.Fatalf("落盘形态应可读回: %v", err)
	}
	byID := map[string]AccessKey{}
	for _, k := range back.Providers[0].AccessKeys {
		byID[k.ID] = k
	}
	if byID["off"].IsEnabled() {
		t.Fatal("enabled=false 未保持")
	}
	if !byID["on"].IsEnabled() || !byID["absent"].IsEnabled() {
		t.Fatalf("true/nil 都应视为启用: %+v", byID)
	}
}

// TestViewEnabledAlwaysEmitted：视图恒输出 enabled（false 不省略），前端据此渲染勾选框与停用标记。
func TestViewEnabledAlwaysEmitted(t *testing.T) {
	f := &File{
		Version: CurrentVersion, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin},
		Providers: []FileProvider{{Platform: "opencode", AccessKeys: []AccessKey{
			{ID: "on", Name: "ON"},
			{ID: "off", Name: "OFF", Enabled: BoolPtr(false)},
		}}},
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
		if v.Providers[0].AccessKeys[1].Enabled {
			t.Fatalf("停用凭据视图应 enabled=false: %+v", v.Providers[0].AccessKeys[1])
		}
	}
}

// TestBuildRuntimeEnabledPointer：BuildRuntime 复制指针（nil 透传），运行期零值不误判为停用。
func TestBuildRuntimeEnabledPointer(t *testing.T) {
	f := &File{
		Version: CurrentVersion, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin},
		Providers: []FileProvider{{Platform: "opencode", AccessKeys: []AccessKey{
			{ID: "absent", Name: "A"},
			{ID: "off", Name: "B", Enabled: BoolPtr(false)},
		}}},
	}
	rt := BuildRuntime(f, nil)
	if !rt.Provider(RuntimeID("opencode", "absent")).IsEnabled() {
		t.Fatal("nil 在运行期应视为启用")
	}
	if rt.Provider(RuntimeID("opencode", "off")).IsEnabled() {
		t.Fatal("false 在运行期应为停用")
	}
	// 零值 RuntimeProvider（测试/新代码常见构造）默认启用，避免「忘记赋值 = 静默停采」
	var zero RuntimeProvider
	if !zero.IsEnabled() {
		t.Fatal("零值 RuntimeProvider 应默认启用")
	}

	// 指针不共享：改动 File 侧不应影响已构建的 Runtime（CopyBoolPtr）
	f2 := &File{
		Version: CurrentVersion, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin},
		Providers: []FileProvider{{Platform: "opencode", AccessKeys: []AccessKey{
			{ID: "p", Name: "P", Enabled: BoolPtr(true)},
		}}},
	}
	rt2 := BuildRuntime(f2, nil)
	*f2.Providers[0].AccessKeys[0].Enabled = false
	if !rt2.Provider(RuntimeID("opencode", "p")).IsEnabled() {
		t.Fatal("Runtime 与 File 不应共享 enabled 指针")
	}
}

// TestBuildRuntimeMultiKey：同平台多凭据展开为多条 RuntimeProvider，KeyIndex/KeyCount 正确，
// 且 base_url/paths 由平台预设派生（配置里不存这些字段）。
func TestBuildRuntimeMultiKey(t *testing.T) {
	f := &File{
		Version: CurrentVersion, Listen: DefaultListen(), Collector: DefaultCollector(),
		Auth: FileAuth{Mode: AuthModeAdmin},
		Providers: []FileProvider{
			{Platform: "opencode", AccessKeys: []AccessKey{{ID: "k1", Name: "A1"}, {ID: "k2", Name: "A2"}, {ID: "k3", Name: "A3"}}},
			{Platform: "deepseek", AccessKeys: []AccessKey{{ID: "d1", Name: "主号"}}},
		},
	}
	rt := BuildRuntime(f, nil)
	if len(rt.Providers) != 4 {
		t.Fatalf("应展开 4 条运行时凭据，实际 %d", len(rt.Providers))
	}
	ids := []string{}
	for _, p := range rt.Providers {
		ids = append(ids, p.ID)
	}
	want := []string{"opencode.k1", "opencode.k2", "opencode.k3", "deepseek.d1"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("运行时 ID 顺序/拼接错误: %v", ids)
		}
	}
	oc := rt.Provider("opencode.k2")
	if oc.KeyIndex != 1 || oc.KeyCount != 3 {
		t.Fatalf("KeyIndex/KeyCount 错误: %+v", oc)
	}
	if oc.BaseURL != "https://opencode.ai/zen/go/v1" || len(oc.Paths) != 1 || oc.Paths[0] != "/usage" {
		t.Fatalf("应由预设派生 base_url/paths: %+v", oc)
	}
	if oc.Name != "OpenCode · A2" {
		t.Fatalf("展示名应为「平台 · 凭据」: %q", oc.Name)
	}
	ds := rt.Provider("deepseek.d1")
	if ds.Name != "DeepSeek · 主号" || ds.Platform != "deepseek" {
		t.Fatalf("deepseek 凭据错误: %+v", ds)
	}
	// 未命名的凭据：展示名回落平台名
	f.Providers[1].AccessKeys[0].Name = ""
	if n := BuildRuntime(f, nil).Provider("deepseek.d1").Name; n != "DeepSeek" {
		t.Fatalf("空凭据名应回落平台名: %q", n)
	}
}

// TestBuildRuntimePresetPaths：四家平台的 base_url/paths 与用户线上配置一致（防手滑改错）。
func TestBuildRuntimePresetPaths(t *testing.T) {
	want := map[string][]string{
		"zhipu-glm": {"/api/monitor/usage/quota/limit"},
		"deepseek":  {"/user/balance"},
		"kimi-code": {"/usages"},
		"opencode":  {"/usage"},
	}
	bases := map[string]string{
		"zhipu-glm": "https://open.bigmodel.cn",
		"deepseek":  "https://api.deepseek.com",
		"kimi-code": "https://api.kimi.com/coding/v1",
		"opencode":  "https://opencode.ai/zen/go/v1",
	}
	for platform, paths := range want {
		preset, ok := PlatformByID(platform)
		if !ok {
			t.Fatalf("预设缺失: %s", platform)
		}
		if preset.BaseURL != bases[platform] {
			t.Fatalf("%s base_url = %q，期望 %q", platform, preset.BaseURL, bases[platform])
		}
		if len(preset.Paths) != len(paths) {
			t.Fatalf("%s paths 数 = %v，期望 %v", platform, preset.Paths, paths)
		}
		for i := range paths {
			if preset.Paths[i] != paths[i] {
				t.Fatalf("%s paths[%d] = %q，期望 %q", platform, i, preset.Paths[i], paths[i])
			}
		}
	}
}
