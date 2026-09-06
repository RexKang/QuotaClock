//go:build windows

package main

import "golang.org/x/sys/windows"

// init 把控制台输出代码页切到 UTF-8：中文 Windows 默认 GBK，会导致 stdout 日志乱码。
func init() {
	_ = windows.SetConsoleOutputCP(65001)
}
