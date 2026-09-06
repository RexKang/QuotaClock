package main

import "testing"

func TestDisplayURL(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"127.0.0.1", 8787, "http://127.0.0.1:8787"},
		{"0.0.0.0", 8787, "http://127.0.0.1:8787"}, // 通配地址落到本机回环
		{"::", 8787, "http://127.0.0.1:8787"},      // IPv6 通配同上
		{"192.168.1.5", 8888, "http://192.168.1.5:8888"},
		{"::1", 8787, "http://[::1]:8787"}, // IPv6 回环保留方括号
	}
	for _, c := range cases {
		if got := displayURL(c.host, c.port); got != c.want {
			t.Errorf("displayURL(%q,%d) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}
