//go:build windows

package main

import "os/exec"

// openBrowser 用系统默认浏览器打开 URL（rundll32 FileProtocolHandler：无额外控制台窗口闪烁）。
// 失败不影响启动（如服务会话无交互桌面），调用方忽略错误并已打印访问地址。
func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
