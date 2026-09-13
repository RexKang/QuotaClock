package config

import (
	"encoding/json"
	"fmt"
)

// 配置导出 / 导入（v0.3.0）。
//
// 为什么不能直接拷贝 config.json：里面的 token 是用**本机 key.bin** 加密的
// （AES-256-GCM），换台机器 key.bin 不同就解不开，等于把 Key 全丢。所以导出给的是
// **可读 JSON + 明文 token**，到目标机器导入时会用那边的 key.bin 重新加密落盘。
//
// 设计上让导出文件**本身就是合法的 PUT 请求体**（多一个 `_note` 说明字段，PUT 容忍并剥离），
// 于是导入不需要新接口：前端解析文件 → 与本地对比出差异给你看 → 确认后走既有保存路径
//（校验、新增 Key 必填、原子写、.bak 备份、热生效全部复用）。

// ExportNote 导出文件顶部的说明（人类可读，导入时被忽略）。
const ExportNote = "QuotaClock 配置导出（v0.3.0）。token 为明文，导入目标机器后会用那台机器的 key.bin 重新加密；" +
	"auth.password 不导出（目标机器沿用其自身密码）。platform 只能用内置的 4 个；" +
	"access_keys[].token 留空 = 保留目标机器上原有的那份。导入完成后请尽快删除本文件。"

// Export 导出文件形态。
type Export struct {
	Note      string           `json:"_note"`
	Version   int              `json:"version"`
	Listen    Listen           `json:"listen"`
	Collector Collector        `json:"collector"`
	Balance   Balance          `json:"balance"`
	Auth      ExportAuth       `json:"auth"`
	Providers []ExportProvider `json:"providers"`
}

// ExportProvider / ExportAccessKey：导出文件里的 provider 形态。
// 与 PUT 的形状一致（导出文件能直接回灌），唯一区别是 `enabled` **一定写出**——
// 导入方就不必靠「缺省 = 启用」这条约定去推断，误删一个字段也不会静默改变启用状态。
type ExportProvider struct {
	Platform   string            `json:"platform"`
	AccessKeys []ExportAccessKey `json:"access_keys"`
}

type ExportAccessKey struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
}

// ExportAuth 导出只带鉴权模式，绝不出密码/hash。
type ExportAuth struct {
	Mode string `json:"mode"`
}

// BuildExport 由落盘配置构造导出体。unseal 负责解密 token（传 nil 或返回空串则该项无 token）。
// 解密失败不阻断导出：该 Key 不带 token（目标机器导入时需自行补），并记入 warnings。
func BuildExport(f *File, unseal func(cipher string) (string, error)) (*Export, []string) {
	ex := &Export{
		Note:      ExportNote,
		Version:   CurrentVersion,
		Listen:    f.Listen,
		Collector: f.Collector,
		Balance:   NormalizeBalance(f.Balance),
		Auth:      ExportAuth{Mode: f.Auth.Mode},
		Providers: []ExportProvider{},
	}
	var warnings []string
	for i := range f.Providers {
		fp := &f.Providers[i]
		pp := ExportProvider{Platform: fp.Platform, AccessKeys: []ExportAccessKey{}}
		for j := range fp.AccessKeys {
			k := &fp.AccessKeys[j]
			// 落盘侧 Enabled 是 *bool（nil = 启用），导出统一折算成显式布尔
			pk := ExportAccessKey{ID: k.ID, Name: k.Name, Enabled: k.Enabled == nil || *k.Enabled}
			if k.TokenCipher == "" {
				// 迁移遗留的空 token（如 v0.1 的 opencode Cookie）：导出同样留空
			} else if unseal == nil {
				warnings = append(warnings, fmt.Sprintf("%s：无解密通道，token 未导出", k.ID))
			} else if plain, err := unseal(k.TokenCipher); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s：token 解密失败，未导出（导入后需重新录入）", k.ID))
			} else {
				pk.Token = plain
			}
			pp.AccessKeys = append(pp.AccessKeys, pk)
		}
		ex.Providers = append(ex.Providers, pp)
	}
	return ex, warnings
}

// MarshalExport 序列化为可读 JSON（两空格缩进，便于手改）。
func MarshalExport(ex *Export) ([]byte, error) {
	b, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
