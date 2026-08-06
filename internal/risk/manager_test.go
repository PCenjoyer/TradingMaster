package risk

import (
	"testing"
	"time"
)

func TestDailyLossLimitBlocksEntriesUntilNextUTCDate(t *testing.T) {
	config := DefaultConfig()
	config.MaxDrawdown = 0.20
	manager, err := NewManager(config, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, time.August, 6, 8, 0, 0, 0, time.UTC)
	manager.UpdateEquity(start, 10_000)
	manager.UpdateEquity(start.Add(time.Hour), 9_690)
	if !manager.Halted(9_690) || manager.HaltReason(9_690) != "превышен дневной лимит убытка" {
		t.Fatalf("дневной лимит не заблокировал входы: %s", manager.HaltReason(9_690))
	}
	if manager.DailyLossStops() != 1 {
		t.Fatalf("ожидалось одно срабатывание, получено %d", manager.DailyLossStops())
	}

	manager.UpdateEquity(start.Add(24*time.Hour), 9_690)
	if manager.Halted(9_690) {
		t.Fatalf("дневная блокировка не снялась: %s", manager.HaltReason(9_690))
	}
}
