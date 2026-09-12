package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleCache() *Cache {
	return &Cache{
		Version:   CacheVersion,
		WrittenAt: "2026-09-12T02:00:00Z",
		Providers: []CacheEntry{
			{ID: "a", Name: "A", Data: json.RawMessage(`{"percentage":42}`), LastSuccessAt: "2026-09-12T01:59:00Z"},
			{ID: "b", Name: "B", Data: json.RawMessage(`"plain-string"`), LastSuccessAt: "2026-09-12T01:58:00Z"},
		},
	}
}

// TestCacheSaveLoadRoundTrip：原子写 → 读回一致；data 保持 JSON 值形态（对象/字符串都不被改写）。
func TestCacheSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := CachePathOf(dir)
	if filepath.Base(path) != CacheName {
		t.Fatalf("缓存路径错误: %s", path)
	}
	if err := SaveCache(path, sampleCache()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + TmpSuffix); !os.IsNotExist(err) {
		t.Fatal("原子写不应残留 .tmp")
	}
	got, err := LoadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != CacheVersion || len(got.Providers) != 2 {
		t.Fatalf("读回内容错误: %+v", got)
	}
	if string(got.Providers[0].Data) != `{"percentage":42}` || string(got.Providers[1].Data) != `"plain-string"` {
		t.Fatalf("data 形态被改写: %s / %s", got.Providers[0].Data, got.Providers[1].Data)
	}
	if got.Providers[0].LastSuccessAt != "2026-09-12T01:59:00Z" {
		t.Fatalf("时间戳未保持: %s", got.Providers[0].LastSuccessAt)
	}
}

// TestCacheLoadEdgeCases：文件缺失 → (nil,nil)；坏 JSON / 版本不符 → 报错（调用方 WARN 后忽略，不阻塞启动）。
func TestCacheLoadEdgeCases(t *testing.T) {
	dir := t.TempDir()
	path := CachePathOf(dir)

	if c, err := LoadCache(path); c != nil || err != nil {
		t.Fatalf("文件不存在应 (nil,nil)，实际 %+v / %v", c, err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCache(path); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"providers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCache(path); err == nil {
		t.Fatal("版本不符应报错")
	}
}

// TestCacheWriterAsyncAndClose：Save 非阻塞（后台合并落盘）；Close 保证最后一份落地。
func TestCacheWriterAsyncAndClose(t *testing.T) {
	dir := t.TempDir()
	path := CachePathOf(dir)
	w := StartCacheWriter(path)

	first := sampleCache()
	first.Providers = first.Providers[:1]
	w.Save(first)

	// 连续投递（模拟一个 tick 内多平台成功）：最终落盘内容 = 最后一份
	second := sampleCache()
	w.Save(second)
	if w.LastError() != nil {
		t.Fatalf("写盘不应失败: %v", w.LastError())
	}
	w.Close()
	w.Close() // 重复 Close 应安全

	got, err := LoadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 2 {
		t.Fatalf("最终内容应为最后一份（2 项），实际 %d", len(got.Providers))
	}

	// Close 之后 Save 不得 panic（关停序列收尾）
	w.Save(sampleCache())
}

// TestCacheWriterCloseFlushesPending：未等到 debounce 也就 Close，待写内容必须落盘。
func TestCacheWriterCloseFlushesPending(t *testing.T) {
	dir := t.TempDir()
	path := CachePathOf(dir)
	w := StartCacheWriter(path)
	w.Save(sampleCache())
	w.Close() // 立即关停，不等 200ms 合并窗口

	got, err := LoadCache(path)
	if err != nil {
		t.Fatalf("Close 应把待写内容落盘: %v", err)
	}
	if len(got.Providers) != 2 {
		t.Fatalf("落盘内容不完整: %+v", got.Providers)
	}

	// 写盘失败路径：目标目录不存在 → LastError 记录，且后续 Save 不 panic
	w2 := StartCacheWriter(filepath.Join(dir, "no-such-dir", CacheName))
	w2.Save(sampleCache())
	time.Sleep(50 * time.Millisecond)
	w2.Close()
	if w2.LastError() == nil {
		t.Fatal("写盘失败应记录 LastError")
	}
}

// TestCacheNoSecrets：缓存只存展示数据，不出现 token 字段（白名单文件的安全复核）。
func TestCacheNoSecrets(t *testing.T) {
	b, err := json.Marshal(sampleCache())
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(b))
	for _, bad := range []string{"token", "cipher", "authorization", "password"} {
		if strings.Contains(s, bad) {
			t.Fatalf("缓存含敏感字段 %q: %s", bad, s)
		}
	}
}
