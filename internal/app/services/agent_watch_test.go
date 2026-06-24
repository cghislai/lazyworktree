package services

import (
	"testing"
	"time"
)

func TestAgentWatchShouldRefresh(t *testing.T) {
	base := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	t.Run("throttles bursts within the debounce window", func(t *testing.T) {
		w := NewAgentWatchService(nil, 600*time.Millisecond, nil)

		if !w.ShouldRefresh(base) {
			t.Fatal("first event should refresh")
		}
		if w.ShouldRefresh(base.Add(100 * time.Millisecond)) {
			t.Fatal("event within debounce window should be throttled")
		}
		if w.ShouldRefresh(base.Add(599 * time.Millisecond)) {
			t.Fatal("event just inside debounce window should be throttled")
		}
		if !w.ShouldRefresh(base.Add(600 * time.Millisecond)) {
			t.Fatal("event at the debounce boundary should refresh")
		}
	})

	t.Run("debounce <= 0 disables throttling", func(t *testing.T) {
		w := NewAgentWatchService(nil, 0, nil)
		for i := 0; i < 5; i++ {
			if !w.ShouldRefresh(base) {
				t.Fatalf("refresh %d should be allowed when throttling is disabled", i)
			}
		}
	})
}
