// Package persist 收敛 QuotaClock 的全部落盘写入（只读硬约束的代码结构落实，设计 §1-2）：
// 白名单 = config.json、key.bin（由 crypto 包写）、quotaclock-*.lock（由 lock 包写）、*.tmp（原子写临时）。
// 其余代码不持有写盘能力。
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
