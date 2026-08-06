package safety

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDailyLossTripsUntilNextUTCTradingDay(t *testing.T) {
	ctx := context.Background()
	controller, err := NewController(Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	if _, _, err := controller.ObserveEquity(ctx, start, 10_000); err != nil {
		t.Fatal(err)
	}
	state, transition, err := controller.ObserveEquity(ctx, start.Add(time.Hour), 9_690)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Active || !state.DailyLimitActive || !transition.Changed {
		t.Fatalf("дневной лимит не сработал: %#v %#v", state, transition)
	}
	if err := controller.CanOpenPosition(ctx); err == nil || !strings.Contains(err.Error(), "заблокировано") {
		t.Fatalf("ожидался запрет новых позиций: %v", err)
	}

	state, transition, err = controller.ObserveEquity(ctx, start.Add(24*time.Hour), 9_700)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active || state.DailyLimitActive || !transition.Changed {
		t.Fatalf("лимит не сброшен в новый UTC-день: %#v %#v", state, transition)
	}
}

func TestManualDeactivateCannotBypassDailyLimit(t *testing.T) {
	ctx := context.Background()
	controller, err := NewController(Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	if _, _, err := controller.ObserveEquity(ctx, now, 10_000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.ObserveEquity(ctx, now.Add(time.Minute), 9_600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.Activate(ctx, "ручная проверка", now); err != nil {
		t.Fatal(err)
	}
	state, _, err := controller.Deactivate(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !state.Active || state.ManualActive || !state.DailyLimitActive {
		t.Fatalf("ручное снятие обошло дневной лимит: %#v", state)
	}
}

func TestInitialFailSafeState(t *testing.T) {
	controller, err := NewController(Config{DailyLossLimit: 0.03, InitialActive: true})
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Active || !state.ManualActive || state.Reason == "" {
		t.Fatalf("контроллер запущен не в fail-safe режиме: %#v", state)
	}
}

func TestStaleEquityCannotResetSafetyState(t *testing.T) {
	ctx := context.Background()
	controller, err := NewController(Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	if _, _, err := controller.ObserveEquity(ctx, now, 10_000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.ObserveEquity(ctx, now.Add(-time.Minute), 20_000); err == nil {
		t.Fatal("устаревшее equity-наблюдение должно быть отклонено")
	}
	state, err := controller.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.DayStartEquity != 10_000 || state.CurrentEquity != 10_000 {
		t.Fatalf("устаревшее наблюдение изменило состояние: %#v", state)
	}
}

func TestOpenPermissionSerializesWithKillSwitch(t *testing.T) {
	controller, err := NewController(Config{DailyLossLimit: 0.03})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- controller.WithOpenPermission(ctx, func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	activationDone := make(chan error, 1)
	go func() {
		_, _, activationErr := controller.Activate(ctx, "параллельная блокировка", time.Now().UTC())
		activationDone <- activationErr
	}()
	select {
	case err := <-activationDone:
		t.Fatalf("kill switch обошёл выполняемую операцию: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-activationDone; err != nil {
		t.Fatal(err)
	}
	if err := controller.CanOpenPosition(ctx); !errors.Is(err, ErrTradingBlocked) {
		t.Fatalf("после активации ожидалась блокировка: %v", err)
	}
}
