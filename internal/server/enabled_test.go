package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RexKang/QuotaClock/internal/collector"
	"github.com/RexKang/QuotaClock/internal/config"
)

// putProvider 一次性 PUT 单平台配置（enabled 显式给值）。
func putProvider(t *testing.T, env *testEnv, ck string, enabled bool) {
	t.Helper()
	body := validPutBody(env)
	body["providers"] = []any{map[string]any{
		"id": "a", "name": "A", "base_url": "https://x.com", "paths": []string{"/"},
		"enabled": enabled,
	}}
	if code, m, _ := env.doJSON("PUT", "/api/config", body, authHdr(ck)); code != 200 {
		t.Fatalf("PUT = %d %v", code, m)
	}
}

// snapshotProvider 取 /api/quotas 里 id=a 的快照项。
func snapshotProvider(t *testing.T, env *testEnv) map[string]any {
	t.Helper()
	code, q, _ := env.doJSON("GET", "/api/quotas", nil, nil)
	if code != 200 {
		t.Fatalf("GET /api/quotas = %d", code)
	}
	provs, _ := q["providers"].([]any)
	if len(provs) != 1 {
		t.Fatalf("快照平台数 = %d", len(provs))
	}
	p, _ := provs[0].(map[string]any)
	return p
}

// TestPutEnabledRoundTrip（v0.2.4）：PUT enabled=false → 落盘 / 视图 / 快照三处一致「已停用」；
// 重新勾选后回到未采集态。
func TestPutEnabledRoundTrip(t *testing.T) {
	env := newEnv(t, nil)
	ck := env.login(config.DefaultPassword)

	// ① 新增平台（默认启用）
	putProvider(t, env, ck, true)
	_, m, _ := env.doJSON("GET", "/api/config", nil, authHdr(ck))
	provs, _ := m["providers"].([]any)
	if len(provs) != 1 {
		t.Fatalf("视图平台数 = %d", len(provs))
	}
	p0, _ := provs[0].(map[string]any)
	if p0["enabled"] != true {
		t.Fatalf("新增平台应默认启用: %v", p0)
	}
	st0 := snapshotProvider(t, env)
	if st0["status"] != "failed" {
		t.Fatalf("启用且未采集 → failed: %v", st0)
	}
	if errObj, ok := st0["error"].(map[string]any); !ok || errObj["code"] != "NOT_COLLECTED_YET" {
		t.Fatalf("启用平台错误码应为 NOT_COLLECTED_YET: %v", st0["error"])
	}

	// ② 取消勾选 → 停用（落盘 / 视图 / 快照三处一致）
	putProvider(t, env, ck, false)
	raw, err := os.ReadFile(env.configPath)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := config.ParseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Providers[0].IsEnabled() {
		t.Fatalf("停用应落盘 enabled=false: %s", raw)
	}
	if !strings.Contains(string(raw), `"enabled": false`) {
		t.Fatalf("落盘应写明 enabled 字段: %s", raw)
	}
	_, m2, _ := env.doJSON("GET", "/api/config", nil, authHdr(ck))
	provs2, _ := m2["providers"].([]any)
	p1, _ := provs2[0].(map[string]any)
	if p1["enabled"] != false {
		t.Fatalf("视图应回传 enabled=false: %v", p1)
	}
	st := snapshotProvider(t, env)
	if st["status"] != "disabled" {
		t.Fatalf("停用平台快照状态应为 disabled: %v", st)
	}
	if errObj, ok := st["error"].(map[string]any); !ok || errObj["code"] != "DISABLED" {
		t.Fatalf("停用平台错误码应为 DISABLED: %v", st["error"])
	}

	// ③ 重新勾选 → 立刻回到未采集态（可采集）
	putProvider(t, env, ck, true)
	raw2, _ := os.ReadFile(env.configPath)
	disk2, err := config.ParseFile(raw2)
	if err != nil {
		t.Fatal(err)
	}
	if !disk2.Providers[0].IsEnabled() {
		t.Fatalf("重新启用应落盘为启用态: %s", raw2)
	}
	if strings.Contains(string(raw2), `"enabled": false`) {
		t.Fatalf("重新启用后不应残留 enabled:false: %s", raw2)
	}
	st2 := snapshotProvider(t, env)
	if errObj, ok := st2["error"].(map[string]any); !ok || errObj["code"] != "NOT_COLLECTED_YET" {
		t.Fatalf("重新启用后应为未采集态: %v", st2)
	}
}

// TestDisabledProviderNeverCollected（v0.2.4 集成）：停用平台在真实调度循环里一次请求都不发，
// 快照状态恒为 disabled；上游 mock 计数为 0。
func TestDisabledProviderNeverCollected(t *testing.T) {
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"success":true,"data":{"percentage":42}}`)
	}))
	defer up.Close()

	env := newEnv(t, nil)
	ck := env.login(config.DefaultPassword)

	// 直连 mock 的停用平台（绕过 https://x.com 占位）
	body := validPutBody(env)
	body["providers"] = []any{map[string]any{
		"id": "a", "name": "A", "base_url": up.URL, "paths": []string{"/q"}, "enabled": false,
	}}
	if code, _, _ := env.doJSON("PUT", "/api/config", body, authHdr(ck)); code != 200 {
		t.Fatal("PUT 失败")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go env.sched.Run(ctx)
	// 覆盖至少一个完整 tick（基准间隔 300s，但 Run 首轮立即执行）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&hits) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	env.sched.Wait()

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("停用平台不应发请求，实际 %d 次", n)
	}
	st := snapshotProvider(t, env)
	if st["status"] != "disabled" {
		t.Fatalf("停用平台状态应保持 disabled: %v", st)
	}
	if _, hasData := st["data"]; hasData && st["data"] != nil {
		t.Fatalf("停用平台不应有数据: %v", st["data"])
	}

	// 重新启用后应立刻开始采集（同一 mock）
	body2 := validPutBody(env)
	body2["providers"] = []any{map[string]any{
		"id": "a", "name": "A", "base_url": up.URL, "paths": []string{"/q"}, "enabled": true,
	}}
	if code, _, _ := env.doJSON("PUT", "/api/config", body2, authHdr(ck)); code != 200 {
		t.Fatal("重新启用 PUT 失败")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go env.sched.Run(ctx2)
	deadline2 := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline2) {
		if atomic.LoadInt32(&hits) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel2()
	env.sched.Wait()
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("重新启用后应开始采集（上游未收到请求）")
	}
	if st := snapshotProvider(t, env); st["status"] != collector.StatusOK {
		t.Fatalf("重新启用并采集成功后应为 ok: %v", st)
	}
}
