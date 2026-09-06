//go:build windows

package lock

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive 是进程运行中的退出码哨兵值（Windows STILL_ACTIVE，x/sys 未导出）。
const stillActive = 259

// probeAlive Windows 探活（D-2 已拍板）：OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION) +
// GetProcessTimes 创建时间比对（PID 复用防护）+ GetExitCodeProcess 终止态检查。
//   - OpenProcess 失败：ERROR_ACCESS_DENIED → 保守存活（他人进程）；其余（含不存在）→ 已死
//   - 已终止（exit code ≠ STILL_ACTIVE，即使句柄残留导致 PID 未释放）→ 已死
//   - GetProcessTimes 失败 → 保守存活
//   - 创建时间 ≠ 锁内 start → 原进程已死、PID 被复用 → stale
func probeAlive(pid int, start int64) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return true, nil
		}
		return false, nil
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err == nil && code != stillActive {
		return false, nil // 进程已终止（含强杀后句柄残留的窗口期）
	}
	var ct, ex, kt, ut windows.Filetime
	if err := windows.GetProcessTimes(h, &ct, &ex, &kt, &ut); err != nil {
		return true, nil
	}
	return filetimeToUnix(ct) == start, nil
}

// selfStartUnix 当前进程创建时刻（unix 秒）。
func selfStartUnix() int64 {
	var ct, ex, kt, ut windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &ct, &ex, &kt, &ut); err != nil {
		return 0
	}
	return filetimeToUnix(ct)
}

func filetimeToUnix(ft windows.Filetime) int64 {
	return ft.Nanoseconds() / 1e9
}
