package config

import "fmt"

// 余额展示设置（v0.3.0）的规范化与校验。
//
// 语义回顾（见 config.go 的 Balance 注释）：把这个数当「满额」，余额 ÷ 满额 = 百分比，
// 颜色按百分比档位取。方向与用量相反——余额百分比越高越健康。

// NormalizeBalance 把零值/缺字段补成默认（旧配置、手写文件、GET 视图共用）。
// 判定「未设置」的口径：Full <= 0 即视为整段没配过，整体回落默认值。
func NormalizeBalance(b Balance) Balance {
	if b.Full <= 0 {
		return DefaultBalance()
	}
	return b
}

// ValidateBalance 余额设置校验（R13）：
//   - full > 0（0 = 未设置，会被规范化成默认值，不报错）
//   - 0 ≤ warn < green ≤ 100（warn == green 允许：那就是两档）
func ValidateBalance(b Balance) []ValidationError {
	var errs []ValidationError
	if b.Full <= 0 {
		return errs // 未设置：规范化补默认，不算错误
	}
	if b.GreenPct < 0 || b.GreenPct > 100 {
		errs = append(errs, ValidationError{Field: "balance.green_pct", Message: fmt.Sprintf("必须在 0~100 之间（当前 %d）", b.GreenPct)})
	}
	if b.WarnPct < 0 || b.WarnPct > 100 {
		errs = append(errs, ValidationError{Field: "balance.warn_pct", Message: fmt.Sprintf("必须在 0~100 之间（当前 %d）", b.WarnPct)})
	}
	if b.WarnPct > b.GreenPct {
		errs = append(errs, ValidationError{Field: "balance.warn_pct", Message: fmt.Sprintf("黄档（%d%%）不能高于绿档（%d%%）", b.WarnPct, b.GreenPct)})
	}
	if b.Full > 1e12 {
		errs = append(errs, ValidationError{Field: "balance.full", Message: "满额基准过大"})
	}
	return errs
}
