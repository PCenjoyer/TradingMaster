package safety

import (
	"fmt"
	"strings"
	"sync"
	"time"
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

type Controller struct {
	mu               sync.RWMutex
	dailyLossLimit   float64
	manualActive     bool
	manualReason     string
	dailyLimitActive bool
	tradingDay       string
	dayStartEquity   float64
	currentEquity    float64
	dailyLossRatio   float64
	updatedAt        time.Time
	lastEquityAt     time.Time
}

func NewController(config Config) (*Controller, error) {
	if config.DailyLossLimit <= 0 || config.DailyLossLimit >= 1 {
		return nil, fmt.Errorf("дневной лимит убытка должен быть в диапазоне (0; 1)")
	}
	controller := &Controller{dailyLossLimit: config.DailyLossLimit}
	if config.InitialActive {
		controller.manualActive = true
		controller.manualReason = strings.TrimSpace(config.InitialReason)
		if controller.manualReason == "" {
			controller.manualReason = "безопасная блокировка при запуске"
		}
		controller.updatedAt = time.Now().UTC()
	}
	return controller, nil
}

func (c *Controller) Activate(reason string, at time.Time) (State, Transition, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return State{}, Transition{}, fmt.Errorf("причина ручной блокировки обязательна")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	c.mu.Lock()
	changed := !c.manualActive || c.manualReason != reason
	c.manualActive = true
	c.manualReason = reason
	c.updatedAt = at.UTC()
	state := c.stateLocked()
	c.mu.Unlock()
	return state, Transition{Changed: changed, Active: state.Active, Engaged: true, Cause: reason, Time: at.UTC()}, nil
}

func (c *Controller) Deactivate(at time.Time) (State, Transition) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	c.mu.Lock()
	changed := c.manualActive
	c.manualActive = false
	c.manualReason = ""
	c.updatedAt = at.UTC()
	state := c.stateLocked()
	cause := "ручная блокировка снята"
	if state.DailyLimitActive {
		cause += "; дневной лимит остаётся активным"
	}
	c.mu.Unlock()
	return state, Transition{Changed: changed, Active: state.Active, Engaged: false, Cause: cause, Time: at.UTC()}
}

func (c *Controller) ObserveEquity(at time.Time, equity float64) (State, Transition, error) {
	if at.IsZero() {
		return State{}, Transition{}, fmt.Errorf("время equity обязательно")
	}
	if equity <= 0 {
		return State{}, Transition{}, fmt.Errorf("equity должно быть положительным")
	}
	at = at.UTC()
	day := at.Format(time.DateOnly)

	c.mu.Lock()
	if !c.lastEquityAt.IsZero() && at.Before(c.lastEquityAt) {
		c.mu.Unlock()
		return State{}, Transition{}, fmt.Errorf("устаревшее equity-наблюдение: последнее время %s", c.lastEquityAt.Format(time.RFC3339))
	}
	wasDailyActive := c.dailyLimitActive
	if c.tradingDay != day {
		c.tradingDay = day
		c.dayStartEquity = equity
		c.dailyLimitActive = false
		c.dailyLossRatio = 0
	}
	c.currentEquity = equity
	c.dailyLossRatio = 0
	if c.dayStartEquity > 0 && equity < c.dayStartEquity {
		c.dailyLossRatio = (c.dayStartEquity - equity) / c.dayStartEquity
	}
	if c.dailyLossRatio >= c.dailyLossLimit {
		c.dailyLimitActive = true
	}
	c.updatedAt = at
	c.lastEquityAt = at
	state := c.stateLocked()

	transition := Transition{Time: at, Active: state.Active}
	if !wasDailyActive && c.dailyLimitActive {
		transition.Changed = true
		transition.Engaged = true
		transition.Cause = fmt.Sprintf(
			"дневной убыток %.2f%% достиг лимита %.2f%%",
			c.dailyLossRatio*100,
			c.dailyLossLimit*100,
		)
	} else if wasDailyActive && !c.dailyLimitActive {
		transition.Changed = true
		transition.Engaged = false
		transition.Cause = "начался новый торговый день; автоматическая блокировка снята"
	}
	c.mu.Unlock()
	return state, transition, nil
}

func (c *Controller) State() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stateLocked()
}

func (c *Controller) CanOpenPosition() error {
	state := c.State()
	if state.Active {
		return fmt.Errorf("новые позиции запрещены: %s", state.Reason)
	}
	return nil
}

func (c *Controller) stateLocked() State {
	active := c.manualActive || c.dailyLimitActive
	reason := ""
	switch {
	case c.manualActive && c.dailyLimitActive:
		reason = c.manualReason + "; превышен дневной лимит убытка"
	case c.manualActive:
		reason = c.manualReason
	case c.dailyLimitActive:
		reason = "превышен дневной лимит убытка"
	}
	return State{
		Active: active, ManualActive: c.manualActive, DailyLimitActive: c.dailyLimitActive,
		Reason: reason, TradingDay: c.tradingDay, DayStartEquity: c.dayStartEquity,
		CurrentEquity: c.currentEquity, DailyLossRatio: c.dailyLossRatio,
		DailyLossLimit: c.dailyLossLimit, UpdatedAt: c.updatedAt,
	}
}
