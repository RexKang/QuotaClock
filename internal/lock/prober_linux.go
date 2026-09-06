//go:build linux

package lock

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// userHZ 是 /proc 接口的时钟粒度：内核 ABI 恒为 100（proc(5) USER_HZ，跨架构稳定）。
// x/sys 新版已移除 linux 的 Sysconf，故按 ABI 常量处理。
const userHZ = 100

// probeAlive unix 探活：kill(pid,0) + /proc/<pid>/stat starttime 比对（PID 复用防护）。
//   - nil=存活；ESRCH=已死；EPERM=存活（他人进程）；未知错误 → 保守存活并上报
func probeAlive(pid int, start int64) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil {
		st := procStartUnix(pid)
		if st < 0 {
			return true, nil // 读取失败保守按存活
		}
		return st == start, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return true, err
}

// selfStartUnix 当前进程创建时刻（unix 秒）= btime + starttime/USER_HZ。
func selfStartUnix() int64 {
	st := procStartUnix(os.Getpid())
	if st < 0 {
		return 0
	}
	btime := bootTimeUnix()
	if btime <= 0 {
		return 0
	}
	return btime + st/userHZ
}

// procStartUnix 返回 /proc/<pid>/stat 第 22 字段 starttime（clock ticks，自开机起）；
// 失败返回 -1。comm 字段可含空格与括号，须从最后一个 ')' 之后切分。
func procStartUnix(pid int) int64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return -1
	}
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		return -1
	}
	fields := strings.Fields(s[idx+1:])
	// fields[0] = state（第 3 字段），starttime 是第 22 字段 → fields[19]
	if len(fields) < 20 {
		return -1
	}
	v, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return -1
	}
	return v
}

func bootTimeUnix() int64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "btime ") {
			v, err := strconv.ParseInt(strings.TrimSpace(line[6:]), 10, 64)
			if err == nil {
				return v
			}
		}
	}
	return -1
}
