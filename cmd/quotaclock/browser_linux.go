//go:build linux

package main

import (
	"os"
	"os/exec"
)

// openBrowser 图形会话下用 xdg-open 打开默认浏览器；
// 无图形环境（SSH/systemd/容器，无 DISPLAY 或 WAYLAND_DISPLAY）跳过，不报错刷屏。
func openBrowser(url string) error {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errNoGUI
	}
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return err
	}
	return exec.Command("xdg-open", url).Start()
}
