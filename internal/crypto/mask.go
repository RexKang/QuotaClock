package crypto

// Mask 生成 token 掩码：前 2 + 后 2 个 rune（按字符不按字节，兼容中文/emoji），
// rune 数 < 4（含空串）输出 "****"，如 ab****ef。掩码不进日志（logx.RegisterSecret 由调用方负责）。
func Mask(token string) string {
	runes := []rune(token)
	n := len(runes)
	if n < 4 {
		return "****"
	}
	return string(runes[:2]) + "****" + string(runes[n-2:])
}
