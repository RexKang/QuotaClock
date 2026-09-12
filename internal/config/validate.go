package config

import (
	"fmt"
	"net"
)

// ValidationError 是一条校验失败项（400 details[] 的元素，不短路——返回全部错误）。
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e ValidationError) String() string { return e.Field + ": " + e.Message }

// Validate 对 Put 做全量校验（PUT 与启动共用；启动路径先经 ParseFile → fileToPut 转换）。
// 返回全部失败项（空切片 = 通过），失败不短路（PRD §4.5）。
//
// v0.2.5 规则调整：provider 不再有用户可配的 base_url/paths/auth_style（平台预设派生），
// 改为校验 platform 白名单与 access_keys 结构。
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
	// R10–R12 providers / platform 白名单 / access_keys
	seenPlatform := map[string]bool{}
	for i := range p.Providers {
		pv := &p.Providers[i]
		field := fmt.Sprintf("providers[%d]", i)
		// R10 platform 必须是预设白名单（不允许自建平台）
		if pv.Platform == "" {
			add(field+".platform", "不能为空")
		} else if !IsKnownPlatform(pv.Platform) {
			add(field+".platform", "不是已知平台（仅支持 %s）", platformIDList())
		} else if seenPlatform[pv.Platform] {
			add(field+".platform", "平台 %q 重复（同一平台只保留一条，多凭据放在 access_keys 里）", pv.Platform)
		}
		seenPlatform[pv.Platform] = true
		// R11 access_keys 至少一个
		if len(pv.AccessKeys) == 0 {
			add(field+".access_keys", "至少需要一个 API Key")
			continue
		}
		// R12 凭据 id 非空且平台内唯一
		seenKey := map[string]bool{}
		for j := range pv.AccessKeys {
			k := &pv.AccessKeys[j]
			kf := fmt.Sprintf("%s.access_keys[%d]", field, j)
			if k.ID == "" {
				add(kf+".id", "不能为空")
			} else if seenKey[k.ID] {
				add(kf+".id", "id %q 重复", k.ID)
			}
			seenKey[k.ID] = true
		}
	}
	return errs
}

// platformIDList 返回受支持平台标识的展示串（错误文案用）。
func platformIDList() string {
	ids := make([]string, 0, len(platforms))
	for _, p := range platforms {
		ids = append(ids, p.ID)
	}
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += " / "
		}
		out += id
	}
	return out
}

// fileToPut 把落盘形态映射为校验用的 Put 形态（同一规则集，避免两套校验逻辑漂移）。
func fileToPut(f *File) *Put {
	p := &Put{
		Version:   f.Version,
		Listen:    f.Listen,
		Collector: f.Collector,
		Auth:      PutAuth{Mode: f.Auth.Mode},
	}
	for i := range f.Providers {
		fp := &f.Providers[i]
		pp := PutProvider{Platform: fp.Platform}
		for j := range fp.AccessKeys {
			k := &fp.AccessKeys[j]
			pp.AccessKeys = append(pp.AccessKeys, PutAccessKey{
				ID:      k.ID,
				Name:    k.Name,
				Enabled: k.Enabled,
			})
		}
		p.Providers = append(p.Providers, pp)
	}
	return p
}

// ValidateFile 校验落盘/启动形态（无 token/password 输入字段），与 Put 校验同一规则集。
func ValidateFile(f *File) []ValidationError { return Validate(fileToPut(f)) }
