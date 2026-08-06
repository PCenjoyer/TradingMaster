package alert

import (
	"context"
	"time"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Event struct {
	Severity Severity
	Title    string
	Message  string
	Time     time.Time
}

type Notifier interface {
	Notify(ctx context.Context, event Event) error
	Enabled() bool
}

type Noop struct{}

func (Noop) Notify(context.Context, Event) error { return nil }
func (Noop) Enabled() bool                       { return false }
