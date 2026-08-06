package safety

import (
	"strings"
	"testing"
	"time"
)

func TestDailyLossTripsUntilNextUTCTradingDay(t *testing.T) {
	controller, err := NewController(Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	if _, _, err := controller.ObserveEquity(start, 10_000); err != nil {
		t.Fatal(err)
	}
	state, transition, err := controller.ObserveEquity(start.Add(time.Hour), 9_690)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Active || !state.DailyLimitActive || !transition.Changed {
		t.Fatalf("дневной лимит не сработал: %#v %#v", state, transition)
	}
	if err := controller.CanOpenPosition(); err == nil || !strings.Contains(err.Error(), "запрещены") {
		t.Fatalf("ожидался запрет новых позиций: %v", err)
	}

	state, transition, err = controller.ObserveEquity(start.Add(24*time.Hour), 9_700)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active || state.DailyLimitActive || !transition.Changed {
		t.Fatalf("лимит не сброшен в новый UTC-день: %#v %#v", state, transition)
	}
}

func TestManualDeactivateCannotBypassDailyLimit(t *testing.T) {
	controller, err := NewController(Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	if _, _, err := controller.ObserveEquity(now, 10_000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.ObserveEquity(now.Add(time.Minute), 9_600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.Activate("ручная проверка", now); err != nil {
		t.Fatal(err)
	}
	state, _ := controller.Deactivate(now.Add(time.Minute))
	if !state.Active || state.ManualActive || !state.DailyLimitActive {
		t.Fatalf("ручное снятие обошло дневной лимит: %#v", state)
	}
}

func TestInitialFailSafeState(t *testing.T) {
	controller, err := NewController(Config{DailyLossLimit: 0.03, InitialActive: true})
	if err != nil {
		t.Fatal(err)
	}
	state := controller.State()
	if !state.Active || !state.ManualActive || state.Reason == "" {
		t.Fatalf("контроллер запущен не в fail-safe режиме: %#v", state)
	}
}

func TestStaleEquityCannotResetSafetyState(t *testing.T) {
	controller, err := NewController(Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	if _, _, err := controller.ObserveEquity(now, 10_000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.ObserveEquity(now.Add(-time.Minute), 20_000); err == nil {
		t.Fatal("устаревшее equity-наблюдение должно быть отклонено")
	}
	state := controller.State()
	if state.DayStartEquity != 10_000 || state.CurrentEquity != 10_000 {
		t.Fatalf("устаревшее наблюдение изменило состояние: %#v", state)
	}
}
