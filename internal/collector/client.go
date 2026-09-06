package collector

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/RexKang/QuotaClock/internal/config"
	"github.com/RexKang/QuotaClock/internal/logx"
)

// OutboundTimeout 出站超时：连接与总时长均 25s（PRD §5，沿用 v0.1 代理值）。
// 变量以便测试注入（C-col-07 用短超时验证超时→NETWORK 分类路径）。
var OutboundTimeout = 25 * time.Second

// maxBodyBytes 出站响应体读取上限（防异常上游拖爆内存）。
const maxBodyBytes = 16 << 20

// Client 出站客户端：UA=QuotaClock/<version>，超时 25s，请求头构造见 §7.6。
type Client struct {
	HTTP    *http.Client
	Version string
}

// NewClient 构造默认出站客户端。
func NewClient(version string) *Client {
	return &Client{HTTP: &http.Client{Timeout: OutboundTimeout}, Version: version}
}

// BuildURL URL 拼接 = 字符串 trim 拼接（D-14：不用 url.Join，避免丢 base 路径前缀）；
// path 含 ?query 原样保留。
func BuildURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// FetchOnce 对 provider 的单个 path 发起 GET 并分类。
// auth_style：bearer → Authorization: Bearer <token>；cookie → Cookie: <token> 且 Accept: */*
// （复刻 v0.1；服务端直连后无浏览器 forbidden-header 限制，X-Cookie 中转整体删除）。
// extra_headers 最后合并、可覆盖默认头（S2）。
func (c *Client) FetchOnce(ctx context.Context, p *config.RuntimeProvider, path string) Classified {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BuildURL(p.BaseURL, path), nil)
	if err != nil {
		return ClassifyTransport(err)
	}
	req.Header.Set("User-Agent", "QuotaClock/"+c.Version)
	if p.AuthStyle == config.AuthStyleCookie {
		req.Header.Set("Accept", "*/*")
		if p.Token != "" {
			req.Header.Set("Cookie", p.Token)
		}
	} else {
		req.Header.Set("Accept", "application/json")
		if p.Token != "" {
			req.Header.Set("Authorization", "Bearer "+p.Token)
		}
	}
	for k, v := range p.ExtraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ClassifyTransport(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return ClassifyTransport(err)
	}
	cl := ClassifyHTTP(resp.StatusCode, resp.Header, body)
	logx.Debugf("fetch %s%s → %s (%d)", p.ID, path, cl.Class, resp.StatusCode)
	return cl
}
