package config

import (
	"github.com/RexKang/QuotaClock/internal/crypto"
)

// BuildRuntime 把落盘形态转为运行时视图：逐 provider 解密 token、生成掩码 hint。
// 解密失败不视为致命（key.bin 与密文不匹配的降级场景，设计 §6.4）：该 provider 标记
// DecryptFailed，采集器将报 token_invalid / TOKEN_DECRYPT_FAILED。
func BuildRuntime(f *File, masterKey []byte) *Runtime {
	r := &Runtime{
		Version:      f.Version,
		Listen:       f.Listen,
		Collector:    f.Collector,
		AuthMode:     f.Auth.Mode,
		PasswordHash: []byte(f.Auth.PasswordHash),
	}
	for i := range f.Providers {
		fp := &f.Providers[i]
		p := &RuntimeProvider{
			ID:           fp.ID,
			Name:         fp.Name,
			BaseURL:      fp.BaseURL,
			Paths:        append([]string(nil), fp.Paths...),
			AuthStyle:    fp.AuthStyle,
			ExtraHeaders: fp.ExtraHeaders,
			TokenCipher:  fp.TokenCipher,
		}
		if p.AuthStyle == "" {
			p.AuthStyle = AuthStyleBearer
		}
		if p.TokenCipher != "" {
			plain, err := crypto.OpenToken(masterKey, p.TokenCipher)
			if err != nil {
				p.DecryptFailed = true
			} else {
				p.Token = plain
				p.TokenMasked = crypto.Mask(plain)
			}
		}
		r.Providers = append(r.Providers, p)
	}
	return r
}

// BuildView 构造 GET /api/config 脱敏视图。
// authenticated：当前请求是否持有效会话——仅登录者可见 token_masked（PRD §6.4）；
// passwordIsDefault：默认密码未修改检测的结果，始终如实返回（契约增量 #1）；
// masks：provider id → 掩码 hint（由调用方在 token 加载/导入/PUT 时从明文计算，
// 设计 §6.5 D-5，避免每次 GET 解密；hint 不落盘，明文/密文/掩码均不落日志）。
func BuildView(f *File, masks map[string]string, authenticated, passwordIsDefault bool) *View {
	v := &View{
		Version:   f.Version,
		Listen:    f.Listen,
		Collector: f.Collector,
		Auth: ViewAuth{
			Mode:              f.Auth.Mode,
			PasswordIsDefault: passwordIsDefault,
			Authenticated:     authenticated,
		},
	}
	for i := range f.Providers {
		fp := &f.Providers[i]
		vp := ViewProvider{
			ID:           fp.ID,
			Name:         fp.Name,
			BaseURL:      fp.BaseURL,
			Paths:        append([]string(nil), fp.Paths...),
			AuthStyle:    fp.AuthStyle,
			ExtraHeaders: fp.ExtraHeaders,
			HasToken:     fp.TokenCipher != "",
		}
		if authenticated && vp.HasToken {
			vp.TokenMasked = masks[fp.ID]
		}
		v.Providers = append(v.Providers, vp)
	}
	return v
}
