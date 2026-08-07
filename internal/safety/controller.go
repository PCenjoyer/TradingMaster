package safety

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrTradingBlocked = errors.New("открытие новых позиций заблокировано")
	ErrStaleEquity    = errors.New("устаревшее equity-наблюдение")
)

type Config struct {
	DailyLossLimit float64
	InitialActive  bool
	InitialReason  string
}

type State struct {
	Active           bool      `json:"active"`
	ManualActive     bool      `json:"manual_active"`
	DailyLimitActive bool      `json:"daily_limit_active"`
	DurableStorage   bool      `json:"durable_storage"`
	Reason           string    `json:"reason"`
	TradingDay       string    `json:"trading_day,omitempty"`
	DayStartEquity   float64   `json:"day_start_equity"`
	CurrentEquity    float64   `json:"current_equity"`
	DailyLossRatio   float64   `json:"daily_loss_ratio"`
	DailyLossLimit   float64   `json:"daily_loss_limit"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Transition struct {
	Changed bool
	Active  bool
	Engaged bool
	Cause   string
	Time    time.Time
}

type storedState struct {
	manualActive     bool
	manualReason     string
	dailyLimitActive bool
	tradingDay       string
	dayStartEquity   float64
	currentEquity    float64
	dailyLossRatio   float64
	dailyLossLimit   float64
	updatedAt        time.Time
	lastEquityAt     time.Time
}

type stateBackend interface {
	Read(context.Context) (storedState, error)
	Update(context.Context, func(*storedState) error) (storedState, error)
	WithLock(context.Context, func(context.Context, storedState) error) error
	Durable() bool
}

type Controller struct {
	backend stateBackend
}

func NewController(config Config) (*Controller, error) {
	initial, err := initialState(config)
	if err != nil {
		return nil, err
	}
	return &Controller{backend: &memoryBackend{state: initial}}, nil
}

func initialState(config Config) (storedState, error) {
	if config.DailyLossLimit <= 0 || config.DailyLossLimit >= 1 {
		return storedState{}, fmt.Errorf("дневной лимит убытка должен быть в диапазоне (0; 1)")
	}
	state := storedState{dailyLossLimit: config.DailyLossLimit}
	if config.InitialActive {
		state.manualActive = true
		state.manualReason = strings.TrimSpace(config.InitialReason)
		if state.manualReason == "" {
			state.manualReason = "безопасная блокировка при запуске"
		}
		state.updatedAt = time.Now().UTC()
	}
	return state, nil
}

func (c *Controller) Activate(ctx context.Context, reason string, at time.Time) (State, Transition, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return State{}, Transition{}, fmt.Errorf("причина ручной блокировки обязательна")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	at = at.UTC()
	changed := false
	stored, err := c.backend.Update(ctx, func(current *storedState) error {
		changed = !current.manualActive || current.manualReason != reason
		current.manualActive = true
		current.manualReason = reason
		current.updatedAt = at
		return nil
	})
	if err != nil {
		return State{}, Transition{}, fmt.Errorf("активировать kill switch: %w", err)
	}
	state := publicState(stored, c.backend.Durable())
	return state, Transition{Changed: changed, Active: state.Active, Engaged: true, Cause: reason, Time: at}, nil
}

func (c *Controller) Deactivate(ctx context.Context, at time.Time) (State, Transition, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	at = at.UTC()
	changed := false
	stored, err := c.backend.Update(ctx, func(current *storedState) error {
		changed = current.manualActive
		current.manualActive = false
		current.manualReason = ""
		current.updatedAt = at
		return nil
	})
	if err != nil {
		return State{}, Transition{}, fmt.Errorf("снять ручной kill switch: %w", err)
	}
	state := publicState(stored, c.backend.Durable())
	cause := "ручная блокировка снята"
	if state.DailyLimitActive {
		cause += "; дневной лимит остаётся активным"
	}
	return state, Transition{Changed: changed, Active: state.Active, Engaged: false, Cause: cause, Time: at}, nil
}

func (c *Controller) ObserveEquity(ctx context.Context, at time.Time, equity float64) (State, Transition, error) {
	if at.IsZero() {
		return State{}, Transition{}, fmt.Errorf("время equity обязательно")
	}
	if equity <= 0 {
		return State{}, Transition{}, fmt.Errorf("equity должно быть положительным")
	}
	at = at.UTC()
	day := at.Format(time.DateOnly)
	transition := Transition{Time: at}

	stored, err := c.backend.Update(ctx, func(current *storedState) error {
		if !current.lastEquityAt.IsZero() && at.Before(current.lastEquityAt) {
			return fmt.Errorf("%w: последнее время %s", ErrStaleEquity, current.lastEquityAt.Format(time.RFC3339))
		}
		wasDailyActive := current.dailyLimitActive
		if current.tradingDay != day {
			current.tradingDay = day
			current.dayStartEquity = equity
			current.dailyLimitActive = false
			current.dailyLossRatio = 0
		}
		current.currentEquity = equity
		current.dailyLossRatio = 0
		if current.dayStartEquity > 0 && equity < current.dayStartEquity {
			current.dailyLossRatio = (current.dayStartEquity - equity) / current.dayStartEquity
		}
		if current.dailyLossRatio >= current.dailyLossLimit {
			current.dailyLimitActive = true
		}
		current.updatedAt = at
		current.lastEquityAt = at

		switch {
		case !wasDailyActive && current.dailyLimitActive:
			transition.Changed = true
			transition.Engaged = true
			transition.Cause = fmt.Sprintf(
				"дневной убыток %.2f%% достиг лимита %.2f%%",
				current.dailyLossRatio*100,
				current.dailyLossLimit*100,
			)
		case wasDailyActive && !current.dailyLimitActive:
			transition.Changed = true
			transition.Cause = "начался новый торговый день; автоматическая блокировка снята"
		}
		return nil
	})
	if err != nil {
		return State{}, Transition{}, fmt.Errorf("обновить equity: %w", err)
	}
	state := publicState(stored, c.backend.Durable())
	transition.Active = state.Active
	return state, transition, nil
}

func (c *Controller) State(ctx context.Context) (State, error) {
	stored, err := c.backend.Read(ctx)
	if err != nil {
		return State{}, fmt.Errorf("прочитать safety-state: %w", err)
	}
	return publicState(stored, c.backend.Durable()), nil
}

func (c *Controller) CanOpenPosition(ctx context.Context) error {
	return c.WithOpenPermission(ctx, func(context.Context) error { return nil })
}

func (c *Controller) WithOpenPermission(ctx context.Context, operation func(context.Context) error) error {
	return c.WithExecutionLock(ctx, true, operation)
}

func (c *Controller) WithExecutionLock(
	ctx context.Context,
	requireOpenPermission bool,
	operation func(context.Context) error,
) error {
	return c.backend.WithLock(ctx, func(operationContext context.Context, stored storedState) error {
		state := publicState(stored, c.backend.Durable())
		if requireOpenPermission && state.Active {
			return fmt.Errorf("%w: %s", ErrTradingBlocked, state.Reason)
		}
		if operation == nil {
			return nil
		}
		return operation(operationContext)
	})
}

func (c *Controller) Durable() bool {
	return c.backend.Durable()
}

func publicState(stored storedState, durable bool) State {
	active := stored.manualActive || stored.dailyLimitActive
	reason := ""
	switch {
	case stored.manualActive && stored.dailyLimitActive:
		reason = stored.manualReason + "; превышен дневной лимит убытка"
	case stored.manualActive:
		reason = stored.manualReason
	case stored.dailyLimitActive:
		reason = "превышен дневной лимит убытка"
	}
	return State{
		Active: active, ManualActive: stored.manualActive, DailyLimitActive: stored.dailyLimitActive,
		DurableStorage: durable, Reason: reason, TradingDay: stored.tradingDay,
		DayStartEquity: stored.dayStartEquity, CurrentEquity: stored.currentEquity,
		DailyLossRatio: stored.dailyLossRatio, DailyLossLimit: stored.dailyLossLimit,
		UpdatedAt: stored.updatedAt,
	}
}

type memoryBackend struct {
	mu    sync.RWMutex
	state storedState
}

func (m *memoryBackend) Read(context.Context) (storedState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state, nil
}

func (m *memoryBackend) Update(_ context.Context, mutation func(*storedState) error) (storedState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := m.state
	if err := mutation(&copy); err != nil {
		return storedState{}, err
	}
	m.state = copy
	return copy, nil
}

func (m *memoryBackend) WithLock(ctx context.Context, operation func(context.Context, storedState) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return operation(ctx, m.state)
}

func (*memoryBackend) Durable() bool { return false }
