package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestExportEnabledAlwaysPresent：enabled 一定要写出来（含 false），
// 导入方不靠「缺省 = 启用」的约定推断 —— 少一个字段不会静默改变启用状态。
func TestExportEnabledAlwaysPresent(t *testing.T) {
	on, off := true, false
	f := &File{Version: CurrentVersion, Auth: FileAuth{Mode: AuthModeNone}, Providers: []FileProvider{
		{Platform: "deepseek", AccessKeys: []AccessKey{
			{ID: "a", Enabled: nil}, {ID: "b", Enabled: &on}, {ID: "c", Enabled: &off},
		}},
	}}
	ex, _ := BuildExport(f, func(string) (string, error) { return "tok", nil })
	raw, err := MarshalExport(ex)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"enabled"`) != 3 {
		t.Errorf("每个 Key 都要有 enabled 字段，实际：%s", raw)
	}
	if !strings.Contains(string(raw), `"enabled": false`) {
		t.Errorf("停用凭据必须显式 enabled:false：%s", raw)
	}
	var back Export
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Providers[0].AccessKeys[0].Enabled || back.Providers[0].AccessKeys[2].Enabled {
		t.Errorf("生效值折算错误：%+v", back.Providers[0].AccessKeys)
	}
}

func sampleV5File() *File {
	on := true
	off := false
	return &File{
		Version:   CurrentVersion,
		Listen:    Listen{Host: "127.0.0.1", Port: 8787},
		Collector: DefaultCollector(),
		Balance:   Balance{Full: 100, GreenPct: 50, WarnPct: 20},
		Auth:      FileAuth{Mode: AuthModeAdmin, PasswordHash: "$2a$10$secret"},
		Providers: []FileProvider{
			{Platform: "deepseek", AccessKeys: []AccessKey{
				{ID: "deepseek", Name: "", TokenCipher: "CIPHER-A", Enabled: &on},
			}},
			{Platform: "opencode", AccessKeys: []AccessKey{
				{ID: "opencode-m1", Name: "M1", TokenCipher: "CIPHER-B", Enabled: &off},
				{ID: "opencode-m2", Name: "M2", TokenCipher: "", Enabled: &on}, // 迁移遗留：无 token
			}},
		},
	}
}

// TestBuildExportPlaintext：导出带明文 token、不发密码、无 token 的 Key 留空、
// 各字段齐全（listen/collector/balance/auth.mode/providers）。
func TestBuildExportPlaintext(t *testing.T) {
	f := sampleV5File()
	unsealed := map[string]string{"CIPHER-A": "sk-plain-deepseek", "CIPHER-B": "sk-plain-m1"}
	ex, warns := BuildExport(f, func(cipher string) (string, error) { return unsealed[cipher], nil })
	if len(warns) != 0 {
		t.Fatalf("不应有警告: %v", warns)
	}
	if ex.Version != CurrentVersion || ex.Auth.Mode != AuthModeAdmin {
		t.Fatalf("头部字段不对: %+v", ex)
	}
	if ex.Balance != f.Balance {
		t.Fatalf("balance 未带出: %+v", ex.Balance)
	}
	raw, err := MarshalExport(ex)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"sk-plain-deepseek"`, `"sk-plain-m1"`, `"platform": "deepseek"`, `"_note"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("导出缺少 %s:\n%s", want, s)
		}
	}
	// 红线：绝不能出现密码 hash、密文
	for _, bad := range []string{"$2a$10$secret", "password_hash", "token_cipher", "CIPHER-A\"", "CIPHER-B\""} {
		if strings.Contains(s, bad) {
			t.Fatalf("导出泄露 %s:\n%s", bad, s)
		}
	}
	// 无 token 的 Key：字段省略（导入时语义 = 保留目标机原有）
	ex2, _ := BuildExport(f, func(cipher string) (string, error) { return unsealed[cipher], nil })
	var m map[string]any
	b2, _ := MarshalExport(ex2)
	if err := json.Unmarshal(b2, &m); err != nil {
		t.Fatal(err)
	}
	provs := m["providers"].([]any)
	oc := provs[1].(map[string]any)
	keys := oc["access_keys"].([]any)
	k2 := keys[1].(map[string]any)
	if _, has := k2["token"]; has {
		t.Fatalf("无 token 的 Key 不该有 token 字段: %+v", k2)
	}
	// enabled=false 必须显式写出（omitempty 只吃掉 nil）
	if v, has := keys[0].(map[string]any)["enabled"]; !has || v != false {
		t.Fatalf("停用状态应显式导出为 false: %+v", keys[0])
	}
}

// TestBuildExportDecryptFailureIsSoft：解密失败不阻断导出，该 Key 不带 token 并记警告。
func TestBuildExportDecryptFailureIsSoft(t *testing.T) {
	f := sampleV5File()
	ex, warns := BuildExport(f, func(cipher string) (string, error) {
		if cipher == "CIPHER-B" {
			return "", errors.New("boom")
		}
		return "sk-plain-deepseek", nil
	})
	if len(warns) != 1 || !strings.Contains(warns[0], "opencode-m1") {
		t.Fatalf("应有一条针对 opencode-m1 的警告: %v", warns)
	}
	if ex.Providers[1].AccessKeys[0].Token != "" {
		t.Fatal("解密失败的 Key 不该带 token")
	}
}

// TestExportIsValidPut：导出文件能直接粘回导入框——PUT 解析器容忍 `_note`，
// 其余字段构成合法请求体（这是「导入不需要新接口」的前提）。
func TestExportIsValidPut(t *testing.T) {
	f := sampleV5File()
	ex, _ := BuildExport(f, func(cipher string) (string, error) { return "sk-plain", nil })
	raw, err := MarshalExport(ex)
	if err != nil {
		t.Fatal(err)
	}
	p, perr := ParsePut(raw)
	if perr != nil {
		t.Fatalf("导出文件应能被 PUT 解析: %v\n%s", perr, raw)
	}
	if len(p.Providers) != 2 || p.Providers[0].AccessKeys[0].Token != "sk-plain" {
		t.Fatalf("解析结果不对: %+v", p.Providers)
	}
	// 带 balance 与 version=当前版本 → 直接过校验
	if errs := Validate(p); len(errs) > 0 {
		t.Fatalf("导出文件应直接通过校验: %+v", errs)
	}
}
