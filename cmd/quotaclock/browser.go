package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

// errNoGUI 表示当前无图形会话（Linux 无 DISPLAY/WAYLAND_DISPLAY），跳过自动打开浏览器。
var errNoGUI = errors.New("未检测到图形环境")

// displayURL 把监听地址转成浏览器可访问的 URL：通配地址（0.0.0.0/::）落到 127.0.0.1。
func displayURL(host string, port int) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port)))
}
