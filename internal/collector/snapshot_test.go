package collector

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func mkStore() *Store { return NewStore("test") }

func provState(id string, status string, data string, last *time.Time, err *ErrorInfo) ProviderState {
	return ProviderState{ID: id, Name: id + "-n", Status: status, Data: json.RawMessage(data), LastSuccessAt: last, Error: err}
}

func cloneStates(s []ProviderState) []ProviderState { return append([]ProviderState(nil), s...) }

var t0 = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func publish(t *testing.T, s *Store, now time.Time, states []ProviderState) bool {
	t.Helper()
	return s.Publish(now, states)
}

func TestPublishInitial(t *testing.T) {
	s := mkStore()
	snap := s.Get()
	if snap.Revision != 0 || len(snap.Providers) != 0 {
		t.Fatalf("初始快照应为 revision 0 空: %+v", snap)
	}
	states := []ProviderState{provState("a", StatusFailed, "null", nil, &ErrorInfo{Code: CodeNotCollectedYet, Message: "尚未完成首次采集"})}
	if !publish(t, s, t0, states) {
		t.Fatal("首次发布应发生")
	}
	if s.Get().Revision != 1 {
		t.Fatalf("首次变化 revision = %d, want 1", s.Get().Revision)
	}
}

// baseStates 两个 provider 的基准快照态。
func baseStates() []ProviderState {
	last := t0.Add(-time.Minute)
	return []ProviderState{
		provState("a", StatusOK, `{"v":1}`, &last, nil),
		provState("b", StatusFailed, "null", nil, &ErrorInfo{Code: "X", Message: "m"}),
	}
}

func TestDiffTriggers(t *testing.T) { // C-snap-01..05：五类触发逐一 revision+1
	cases := []struct {
		name   string
		mutate func(*[]ProviderState)
	}{
		{"① data 变化", func(s *[]ProviderState) { (*s)[0].Data = json.RawMessage(`{"v":2}`) }},
		{"② status 变化", func(s *[]ProviderState) { (*s)[0].Status = StatusTokenInvalid }},
		{"③ error message 变化", func(s *[]ProviderState) { (*s)[1].Error = &ErrorInfo{Code: "X", Message: "m2"} }},
		{"③ error retry_after 变化", func(s *[]ProviderState) {
			r := 5
			(*s)[1].Error = &ErrorInfo{Code: "X", Message: "m", RetryAfterS: &r}
		}},
		{"④ last_success_at 刷新", func(s *[]ProviderState) { nt := t0; (*s)[0].LastSuccessAt = &nt }},
		{"④ 成功但 data 无变化", func(s *[]ProviderState) { nt := t0.Add(time.Second); (*s)[0].LastSuccessAt = &nt }},
		{"⑤ 改名", func(s *[]ProviderState) { (*s)[0].Name = "a-n2" }},
		{"⑤ 增加", func(s *[]ProviderState) {
			*s = append(*s, provState("c", StatusFailed, "null", nil, &ErrorInfo{Code: CodeNotCollectedYet, Message: "x"}))
		}},
		{"⑤ 删除", func(s *[]ProviderState) { *s = (*s)[:1] }},
		{"⑤ 换序", func(s *[]ProviderState) { (*s)[0], (*s)[1] = (*s)[1], (*s)[0] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mkStore()
			base := baseStates()
			publish(t, s, t0, base)
			rev := s.Get().Revision
			mutated := cloneStates(base)
			tc.mutate(&mutated)
			if !publish(t, s, t0.Add(time.Second), mutated) {
				t.Fatal("应触发发布")
			}
			if s.Get().Revision != rev+1 {
				t.Fatalf("revision = %d, want %d", s.Get().Revision, rev+1)
			}
			want := t0.Add(time.Second).Truncate(time.Second).UTC()
			if got := s.Get().CollectedAt; got.Unix() != want.Unix() {
				t.Fatalf("collected_at 应同节奏更新: %v want %v", got, want)
			}
		})
	}
}

func TestDiffNoChange(t *testing.T) { // C-snap-06：全字段无变化 → revision 不动、指针不换
	s := mkStore()
	base := baseStates()
	publish(t, s, t0, base)
	ptr1 := s.Get()
	if publish(t, s, t0.Add(time.Hour), cloneStates(base)) {
		t.Fatal("无变化不应发布")
	}
	if s.Get() != ptr1 {
		t.Fatal("指针不应更换")
	}
	if s.Get().Revision != 1 {
		t.Fatalf("revision = %d", s.Get().Revision)
	}
}

func TestSnapshotNullInvariants(t *testing.T) { // C-snap-07 空值三条式
	s := mkStore()
	// ① 从未成功：data/last_success null 且 error 非空
	st1 := []ProviderState{provState("a", StatusFailed, "null", nil, &ErrorInfo{Code: CodeNotCollectedYet, Message: "尚未完成首次采集"})}
	publish(t, s, t0, st1)
	b, _ := json.Marshal(s.Get())
	s1 := string(b)
	if !contains(s1, `"data":null`) || !contains(s1, `"last_success_at":null`) {
		t.Fatalf("从未成功应双 null: %s", s1)
	}
	// ③ 曾成功后失败：保留最近成功值
	last := t0
	publish(t, s, t0, []ProviderState{provState("a", StatusOK, `{"v":1}`, &last, nil)})
	r := 60
	st3 := []ProviderState{provState("a", StatusFailed, `{"v":1}`, &last, &ErrorInfo{Code: CodeNetwork, Message: "网络错误或请求超时", RetryAfterS: &r})}
	publish(t, s, t0, st3)
	b2, _ := json.Marshal(s.Get())
	s2 := string(b2)
	if contains(s2, `"data":null`) || contains(s2, `"last_success_at":null`) {
		t.Fatalf("曾成功后失败应保留数据: %s", s2)
	}
	if !contains(s2, `"code":"NETWORK_ERROR"`) {
		t.Fatalf("失败态 error 应反映当前: %s", s2)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestTimestampFormat(t *testing.T) { // C-snap-08：RFC3339 UTC 秒级（Z 结尾无小数）
	s := mkStore()
	last := time.Date(2026, 9, 5, 12, 30, 15, 999999999, time.UTC)
	st := []ProviderState{provState("a", StatusOK, `1`, &last, nil)}
	publish(t, s, last, st)
	b, _ := json.Marshal(s.Get())
	var w struct {
		CollectedAt string `json:"collected_at"`
		Providers   []struct {
			LastSuccessAt *string `json:"last_success_at"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	if w.CollectedAt != "2026-09-05T12:30:15Z" {
		t.Fatalf("collected_at = %q", w.CollectedAt)
	}
	if w.Providers[0].LastSuccessAt == nil || *w.Providers[0].LastSuccessAt != "2026-09-05T12:30:15Z" {
		t.Fatalf("last_success_at = %v", w.Providers[0].LastSuccessAt)
	}
}

func TestRestartRevision(t *testing.T) { // C-snap-09：重启归 0→1；单次重建多变化合并为 +1
	s := mkStore()
	last := t0
	st := []ProviderState{provState("a", StatusOK, `{"v":1}`, &last, nil)}
	publish(t, s, t0, st)
	// 同一次 Publish 里同时改 data + status + error → 仍只 +1
	r := 9
	merged := []ProviderState{provState("a", StatusFailed, `{"v":9}`, &last, &ErrorInfo{Code: CodeBizError, Message: "x", RetryAfterS: &r})}
	if !publish(t, s, t0, merged) {
		t.Fatal("应发布")
	}
	if s.Get().Revision != 2 {
		t.Fatalf("单次重建多变化应合并 +1: %d", s.Get().Revision)
	}
	s2 := mkStore()
	publish(t, s2, t0, st)
	if s2.Get().Revision != 1 {
		t.Fatalf("重启后首个快照 revision = %d", s2.Get().Revision)
	}
}

func TestConcurrentAccess(t *testing.T) { // C-snap-10：-race 下 1 写 N 读压测
	s := mkStore()
	last := t0
	states := []ProviderState{provState("a", StatusOK, `{"v":1}`, &last, nil)}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				b, _ := json.Marshal(s.Get())
				_ = b
			}
		}()
	}
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			ns := cloneStates(states)
			ns[0].Data = json.RawMessage(fmt.Sprintf(`{"v":%d}`, i))
			s.Publish(time.Now(), ns)
		}
	}()
	<-done
	wg.Wait()
}
