package persist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackupConfigRawCopy：备份内容 = 原文件原始字节（不重新序列化），命名带旧版本号。
func TestBackupConfigRawCopy(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ConfigName)
	// 故意用「非规范」内容：缩进奇怪 + 中文 + 字段顺序乱 —— 备份必须逐字节相同
	raw := "{\n   \"version\" : 3,\n\t\"note\": \"手工维护备注\",\n  \"providers\": []\n}\n"
	if err := os.WriteFile(cfg, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	dest, err := BackupConfig(cfg, 3)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dest) != "config.json.v3.bak" {
		t.Fatalf("备份名不符合约定: %s", dest)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != raw {
		t.Fatalf("备份内容应与原文件逐字节一致:\n%q\n%q", string(got), raw)
	}
	// 原文件未被改动
	orig, _ := os.ReadFile(cfg)
	if string(orig) != raw {
		t.Fatal("备份不应改动原文件")
	}
	// 无 tmp 残留
	if _, err := os.Stat(dest + TmpSuffix); !os.IsNotExist(err) {
		t.Fatal("备份后残留 tmp")
	}
}

// TestBackupConfigNoClobber：同名备份已存在时追加序号，绝不覆盖既有备份。
func TestBackupConfigNoClobber(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(cfg, []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := BackupConfig(cfg, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 改动原文件后再备份一次（模拟用户回退版本后又升级）
	if err := os.WriteFile(cfg, []byte(`{"version":3,"note":"second"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := BackupConfig(cfg, 3)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("第二次备份不应覆盖第一次")
	}
	if filepath.Base(second) != "config.json.v3-2.bak" {
		t.Fatalf("重复备份命名不符: %s", second)
	}
	b1, _ := os.ReadFile(first)
	if !strings.Contains(string(b1), `"version":3`) || strings.Contains(string(b1), "second") {
		t.Fatalf("第一份备份被改写: %s", b1)
	}
	b2, _ := os.ReadFile(second)
	if !strings.Contains(string(b2), "second") {
		t.Fatalf("第二份备份内容错误: %s", b2)
	}
}

// TestBackupConfigMissingSource：源文件不存在 → 报错（调用方据此中止迁移）。
func TestBackupConfigMissingSource(t *testing.T) {
	dir := t.TempDir()
	if _, err := BackupConfig(filepath.Join(dir, "nope.json"), 3); err == nil {
		t.Fatal("源文件缺失应报错")
	}
}

// TestIsBackupFile：白名单判定（用于写盘审计与文档口径）。
func TestIsBackupFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"config.json.v3.bak", true},
		{"config.json.v2.bak", true},
		{"config.json.v3-2.bak", true},
		{filepath.Join("C:", "data", "config.json.v3.bak"), true},
		{"config.json.bak", false}, // 缺版本号
		{"config.json.v3.bakx", false},
		{"config.json.vX.bak", false},
		{"config.json", false},
		{"cache.json", false},
	}
	for _, tc := range cases {
		if got := IsBackupFile(tc.name); got != tc.want {
			t.Fatalf("IsBackupFile(%q) = %v，期望 %v", tc.name, got, tc.want)
		}
	}
}
