package config

import "fmt"

// ValidateNewKeys 校验「新增凭据必须带 token」（v0.2.5 的 PUT 门）。
//
// 为什么需要：PUT 是全量替换的回传素材，**已存在的凭据**允许 token 留空（= 保留原密文），
// 但新加的凭据没有可保留的密文——留空只会得到一个必然失败的凭据（401 → token 失效），
// 用户看到的就是「加了 Key 却一直是红的」。UI 已挡住这条路，这里做后端兜底。
//
// 只拦「新增 + 无 token」：
//   - 迁移产出的空 token 凭据（v0.1 的 opencode Cookie 未迁移）本身就已存在于旧配置，
//     不受影响——否则用户填好别的平台后反而无法保存。
//   - 删除凭据、改名字、改启用状态、改间隔等都不受此规则影响。
func ValidateNewKeys(put *Put, old *File) []ValidationError {
	if put == nil || old == nil {
		return nil
	}
	existing := map[string]bool{}
	for i := range old.Providers {
		fp := &old.Providers[i]
		for j := range fp.AccessKeys {
			// 注意：即使密文为空也算「已存在」——见上面迁移场景的说明
			existing[RuntimeID(fp.Platform, fp.AccessKeys[j].ID)] = true
		}
	}
	var errs []ValidationError
	for i := range put.Providers {
		pp := &put.Providers[i]
		for j := range pp.AccessKeys {
			pk := &pp.AccessKeys[j]
			if pk.Token != "" {
				continue
			}
			if !existing[RuntimeID(pp.Platform, pk.ID)] {
				errs = append(errs, ValidationError{
					Field:   fmt.Sprintf("providers[%d].access_keys[%d].token", i, j),
					Message: "新增的 API Key 必须填写内容（名称可留空）",
				})
			}
		}
	}
	return errs
}
