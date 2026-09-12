// Package persist 收敛 QuotaClock 的全部落盘写入（只读硬约束的代码结构落实，设计 §1-2）：
// 白名单 = config.json、key.bin（由 crypto 包写）、quotaclock-*.lock（由 lock 包写）、
// cache.json（上次成功数据缓存，v0.2.4 新增）、<config>.v<N>.bak（迁移前备份，v0.2.5 新增）、
// *.tmp（原子写临时）。其余代码不持有写盘能力。
package persist

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 运行时文件白名单（文件名常量在此收口）。
const (
	ConfigName = "config.json"
	KeyName    = "key.bin"
	LockPrefix = "quotaclock-"
	LockSuffix = ".lock"
	TmpSuffix  = ".tmp"
)

// configPerm 配置文件权限（Windows 无 POSIX 位，尽力而为）。
const configPerm = 0o600

// AtomicWrite 原子写：同目录 <target>.tmp（0600）→ Sync → rename 覆盖。
// 失败路径：tmp 写失败 → 删 tmp、目标未动；rename 失败 → 目标未动、tmp 残留（下次启动清理）。
func AtomicWrite(path string, data []byte) error {
	return atomicWriteWith(path, data, os.Rename)
}

func atomicWriteWith(path string, data []byte, rename func(old, new string) error) error {
	tmp := path + TmpSuffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, configPerm)
	if err != nil {
		return fmt.Errorf("原子写创建临时文件失败: %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("原子写写入失败: %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("原子写 Sync 失败: %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("原子写关闭失败: %s: %w", tmp, err)
	}
	if err := rename(tmp, path); err != nil {
		// rename 失败：目标未动，tmp 残留（下次启动清理）
		return fmt.Errorf("原子写 rename 失败: %s → %s: %w", tmp, path, err)
	}
	return nil
}

// CleanTmp 清理配置目录下所有 *.tmp 残留（启动时、先于任何原子写调用，设计 §10.4）。
func CleanTmp(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*"+TmpSuffix))
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理 tmp 残留失败: %s: %w", m, err)
		}
	}
	return nil
}

// ConfigPathOf 返回目录下的 config.json 路径。
func ConfigPathOf(dir string) string { return filepath.Join(dir, ConfigName) }

// LockNameOf 返回配置绝对路径对应的锁文件名：
// quotaclock-<sha256(配置文件绝对路径) 前 16 位 hex>.lock（PRD §5.1）。
func LockNameOf(configAbsPath string) string {
	return LockPrefix + shortHash(configAbsPath) + LockSuffix
}

// IsLockFile 判断一个路径是否属于锁文件白名单。
func IsLockFile(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, LockPrefix) && strings.HasSuffix(base, LockSuffix)
}

// ---------- 迁移前备份（v0.2.5） ----------

// BackupSuffix 备份文件后缀：<config 文件名>.v<旧版本>.bak（重复迁移时再追加 -2/-3…）。
// 已被 .gitignore 的 *.bak 覆盖（备份内含 token 密文，绝不可入库）。
const BackupSuffix = ".bak"

// BackupConfig 在迁移改写落盘**之前**把原配置文件原样备份一份，返回备份路径。
//
// 为什么要备份：迁移是不可逆的结构改写（v0.1/v0.2.x → 平台预设结构），一旦写盘就
// 只剩新结构；旧文件里可能还有用户手工维护的信息（旧 paths、备注、停用状态）。
// 备份按旧版本号命名，便于人工核对与回退；同名时追加 -2/-3…，绝不覆盖既有备份。
//
// 内容是原文件的**原始字节**（不重新序列化）——备份要能代表「改写前它长什么样」。
// 注意：v0.1（version 2）的备份会保留明文 token（原文件本就是明文），
// 调用方应提示用户核对后删除。
func BackupConfig(path string, oldVersion int) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取原配置失败: %s: %w", path, err)
	}
	dest := fmt.Sprintf("%s.v%d%s", path, oldVersion, BackupSuffix)
	for i := 2; ; i++ {
		if _, serr := os.Stat(dest); os.IsNotExist(serr) {
			break
		}
		dest = fmt.Sprintf("%s.v%d-%d%s", path, oldVersion, i, BackupSuffix)
	}
	if err := AtomicWrite(dest, raw); err != nil {
		return "", fmt.Errorf("写出备份失败: %s: %w", dest, err)
	}
	return dest, nil
}

// IsBackupFile 判断一个路径是否属于备份文件白名单（<config>.v<N>.bak / <config>.v<N>-<K>.bak）。
func IsBackupFile(path string) bool {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, BackupSuffix) {
		return false
	}
	rest := strings.TrimSuffix(base, BackupSuffix)
	i := strings.LastIndex(rest, ".v")
	if i < 0 {
		return false
	}
	num := rest[i+2:]
	if dash := strings.LastIndex(num, "-"); dash >= 0 {
		num = num[:dash]
	}
	if num == "" {
		return false
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
