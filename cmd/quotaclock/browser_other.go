//go:build !windows && !linux

package main

// openBrowser 其余平台（构建矩阵外）不做自动打开，只打印访问地址。
func openBrowser(url string) error { return errNoGUI }
