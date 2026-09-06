//go:build !linux && !windows

package lock

import "syscall"

// probeAlive 其他 unix（如 darwin）无 /proc：仅 kill(pid,0) 探活（无 PID 复用防护，保守处理）。
func probeAlive(pid int, start int64) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if err == syscall.ESRCH {
		return false, nil
	}
	return true, nil // EPERM 等一律保守存活
}

// selfStartUnix 无创建时间来源，返回 0（写锁时与探活同源，自检语义不变）。
func selfStartUnix() int64 { return 0 }
