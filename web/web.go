// Package web 内嵌静态页面与图标（交付形态为单二进制）。
package web

import "embed"

//go:embed index.html
var files embed.FS

//go:embed icons/favicon.ico icons/favicon.svg icons/favicon-16.png icons/favicon-32.png icons/favicon-48.png icons/favicon-64.png icons/favicon-128.png icons/favicon-256.png
var iconFS embed.FS

// IconNames 内嵌图标的文件名白名单（favicon-sheet.png 是设计预览稿，不内嵌不外发）。
var IconNames = []string{
	"favicon.ico", "favicon.svg",
	"favicon-16.png", "favicon-32.png", "favicon-48.png",
	"favicon-64.png", "favicon-128.png", "favicon-256.png",
}

// Index 返回内嵌的 index.html 内容。
func Index() ([]byte, error) {
	return files.ReadFile("index.html")
}

// Icon 按白名单文件名返回内嵌图标内容；未命中返回 false。
func Icon(name string) ([]byte, bool) {
	for _, n := range IconNames {
		if n == name {
			b, err := iconFS.ReadFile("icons/" + name)
			if err != nil {
				return nil, false
			}
			return b, true
		}
	}
	return nil, false
}

// Icons 返回全部内嵌图标（文件名 → 内容），供服务端装配。
func Icons() map[string][]byte {
	m := make(map[string][]byte, len(IconNames))
	for _, n := range IconNames {
		if b, ok := Icon(n); ok {
			m[n] = b
		}
	}
	return m
}
