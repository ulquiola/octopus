package op

import (
	"context"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

// TestGroupRefreshCacheNormalizesRelayConfig 复现旧分组 relay_config 缺少新字段时,
// 启动加载后首事件超时被当成 0 秒的问题。
func TestGroupRefreshCacheNormalizesRelayConfig(t *testing.T) {
	groupCache.Clear()
	t.Cleanup(func() {
		groupCache.Clear()
		_ = db.Close()
	})
	if err := db.InitDB("sqlite", t.TempDir()+"/test.db", false); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Exec(
		`INSERT INTO groups (name, mode, active_item_id, relay_config) VALUES (?, ?, ?, ?)`,
		"legacy-group", "failover", 0, `{"member_retry_interval_seconds":1}`,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := groupRefreshCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	group, ok := groupCache.Get(1)
	if !ok {
		t.Fatal("group not cached")
	}
	want := model.DefaultGroupRelayConfig()
	if group.RelayConfig.MemberStreamFirstEventTimeoutSeconds != want.MemberStreamFirstEventTimeoutSeconds {
		t.Fatalf("stream first event timeout = %d, want %d",
			group.RelayConfig.MemberStreamFirstEventTimeoutSeconds,
			want.MemberStreamFirstEventTimeoutSeconds)
	}
	if group.RelayConfig.MemberNonStreamResponseTimeoutSeconds != want.MemberNonStreamResponseTimeoutSeconds {
		t.Fatalf("non-stream response timeout = %d, want %d",
			group.RelayConfig.MemberNonStreamResponseTimeoutSeconds,
			want.MemberNonStreamResponseTimeoutSeconds)
	}
}
