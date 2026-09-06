package config

// sealForTest 测试桩：视图构建不解密 token_cipher，任意非空字符串即可充当密文占位
// （加解密往返语义已在 crypto 包覆盖）。
func sealForTest(plain string) (string, error) {
	return "fake-cipher-" + plain, nil
}
