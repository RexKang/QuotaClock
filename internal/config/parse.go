package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// putWire 是 PUT 请求体的完整 wire 形态：包含计算/只读字段（剥离忽略）与敏感字段（出现即 400），
// 以便 DisallowUnknownFields 的同时仍能识别这两类特殊字段。
//
// v0.2.5：base_url / paths / auth_style / extra_headers 由平台预设派生，**不是可配置项**；
// 但 GET 响应会把它们（连同 platform_name）带给前端展示，整体回传时必然出现，
// 故一律容忍并剥离——平台预设是唯一真源，回传值不参与任何判断。
type putWire struct {
	Version   int           `json:"version"`
	Listen    Listen        `json:"listen"`
	Collector Collector     `json:"collector"`
	Auth      putAuthWire   `json:"auth"`
	Providers []putProvWire `json:"providers"`
}

type putAuthWire struct {
	Mode              string `json:"mode"`
	Password          string `json:"password,omitempty"`
	PasswordHash      string `json:"password_hash,omitempty"`       // 敏感字段：出现即 400
	PasswordIsDefault bool   `json:"password_is_default,omitempty"` // 计算字段：剥离
	Authenticated     bool   `json:"authenticated,omitempty"`       // 计算字段：剥离
}

type putProvWire struct {
	Platform   string       `json:"platform"`
	AccessKeys []putKeyWire `json:"access_keys"`
	// 只读/派生字段：容忍并剥离（见类型注释）
	PlatformName string            `json:"platform_name,omitempty"`
	BaseURL      string            `json:"base_url,omitempty"`
	Paths        []string          `json:"paths,omitempty"`
	AuthStyle    string            `json:"auth_style,omitempty"`
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
	ID           string            `json:"id,omitempty"`
	Name         string            `json:"name,omitempty"`
	// 敏感字段：出现即 400
	TokenCipher string `json:"token_cipher,omitempty"`
}

type putKeyWire struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Token       string `json:"token,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	HasToken    bool   `json:"has_token"`              // 计算字段：剥离
	TokenMasked string `json:"token_masked,omitempty"` // 计算字段：剥离
}

// ParsePut 解析 PUT /api/config 请求体。
// 解析门顺序（设计 §5.3）：JSON 可解析（DisallowUnknownFields）→ 剥离计算/只读字段 → 拒绝敏感字段。
// 返回的 error 已带 field 定位，由 handler 包装为 400 details[]。
func ParsePut(body []byte) (*Put, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var w putWire
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("JSON 解析失败或含未知字段: %v", err)
	}
	p := &Put{
		Version:   w.Version,
		Listen:    w.Listen,
		Collector: w.Collector,
		Auth:      PutAuth{Mode: w.Auth.Mode, Password: w.Auth.Password},
	}
	if w.Auth.PasswordHash != "" {
		return nil, fmt.Errorf("auth.password_hash: 禁止直写敏感字段")
	}
	for i := range w.Providers {
		wp := &w.Providers[i]
		if wp.TokenCipher != "" {
			return nil, fmt.Errorf("providers[%d].token_cipher: 禁止直写敏感字段", i)
		}
		pp := PutProvider{Platform: wp.Platform}
		for j := range wp.AccessKeys {
			wk := &wp.AccessKeys[j]
			pp.AccessKeys = append(pp.AccessKeys, PutAccessKey{
				ID:      wk.ID,
				Name:    wk.Name,
				Token:   wk.Token,
				Enabled: wk.Enabled,
			})
		}
		p.Providers = append(p.Providers, pp)
	}
	return p, nil
}

// ParseFile 解析落盘形态（DisallowUnknownFields 同样生效），供启动加载使用。
// version 的缺失与非法由调用方通过 ProbeVersion 区分处置。
func ParseFile(body []byte) (*File, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("JSON 解析失败或含未知字段: %v", err)
	}
	return &f, nil
}

// ProbeVersion 从原始 JSON 提取 version 字段；缺失返回 has=false。
func ProbeVersion(body []byte) (version int, has bool, err error) {
	var probe struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return 0, false, err
	}
	if probe.Version == nil {
		return 0, false, nil
	}
	return *probe.Version, true, nil
}
