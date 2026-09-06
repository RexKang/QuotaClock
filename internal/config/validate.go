package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
)

// ValidationError 是一条校验失败项（400 details[] 的元素，不短路——返回全部错误）。
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e ValidationError) String() string { return e.Field + ": " + e.Message }

var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// Validate 对 Put 做 R1–R14 全量校验（PUT 与启动共用；启动路径先经 ParsePutFile 转换）。
// 返回全部失败项（空切片 = 通过），失败不短路（PRD §4.5）。
func Validate(p *Put) []ValidationError {
	var errs []ValidationError
	add := func(field, format string, args ...any) {
		errs = append(errs, ValidationError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	// R1 version
	if p.Version != CurrentVersion {
		add("version", "必须为 %d", CurrentVersion)
	}
	// R2 port
	if p.Listen.Port < 1 || p.Listen.Port > 65535 {
		add("listen.port", "必须在 [1,65535] 内")
	}
	// R3 host
	if net.ParseIP(p.Listen.Host) == nil {
		add("listen.host", "必须是合法 IP 地址")
	}
	// R4 interval
	if p.Collector.IntervalBaseS < 30 {
		add("collector.interval_base_s", "必须 ≥ 30")
	}
	// R5 jitter
	if p.Collector.JitterMinS > p.Collector.JitterMaxS {
		add("collector.jitter_min_s", "必须 ≤ jitter_max_s")
	}
	// R6 stagger
	if p.Collector.StaggerMinS > p.Collector.StaggerMaxS {
		add("collector.stagger_min_s", "必须 ≤ stagger_max_s")
	}
	// R7 backoff multiplier
	if p.Collector.BackoffMultiplier < 1 || p.Collector.BackoffMultiplier > 8 {
		add("collector.backoff_multiplier", "必须在 [1,8] 内")
	}
	// R8 backoff max
	if p.Collector.BackoffMaxS > 86400 {
		add("collector.backoff_max_s", "必须 ≤ 86400")
	}
	// R9 auth mode
	if p.Auth.Mode != AuthModeAdmin && p.Auth.Mode != AuthModeNone {
		add("auth.mode", "必须是 admin 或 none")
	}
	// R10-R12, R13, R14 providers
	seen := map[string]bool{}
	for i := range p.Providers {
		pv := &p.Providers[i]
		field := fmt.Sprintf("providers[%d]", i)
		if pv.ID == "" {
			add(field+".id", "不能为空")
		} else if seen[pv.ID] {
			add(field+".id", "id %q 重复", pv.ID)
		}
		seen[pv.ID] = true
		if u, err := url.Parse(pv.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add(field+".base_url", "必须是合法的 http(s) URL")
		}
		if len(pv.Paths) == 0 {
			add(field+".paths", "不能为空数组")
		}
		// R13 auth_style（缺省 bearer）
		switch pv.AuthStyle {
		case "", AuthStyleBearer, AuthStyleCookie:
		default:
			add(field+".auth_style", "必须是 bearer 或 cookie")
		}
		// R14 extra_headers 键名
		for k := range pv.ExtraHeaders {
			if !headerNameRe.MatchString(k) {
				add(field+".extra_headers", "header 名 %q 非法（须匹配 ^[A-Za-z0-9-]+$）", k)
			}
		}
	}
	return errs
}

// ValidateFile 校验落盘/启动形态（无 token/password 输入字段），与 Put 校验同一规则集。
func ValidateFile(f *File) []ValidationError {
	p := &Put{
		Version:   f.Version,
		Listen:    f.Listen,
		Collector: f.Collector,
		Auth:      PutAuth{Mode: f.Auth.Mode},
	}
	for i := range f.Providers {
		fp := &f.Providers[i]
		p.Providers = append(p.Providers, PutProvider{
			ID:           fp.ID,
			Name:         fp.Name,
			BaseURL:      fp.BaseURL,
			Paths:        fp.Paths,
			AuthStyle:    fp.AuthStyle,
			ExtraHeaders: fp.ExtraHeaders,
		})
	}
	return Validate(p)
}
