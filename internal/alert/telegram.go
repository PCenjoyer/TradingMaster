package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type TelegramConfig struct {
	BotToken string
	ChatID   string
	BaseURL  string
	Timeout  time.Duration
}

type Telegram struct {
	endpoint string
	chatID   string
	client   *http.Client
}

func NewTelegram(config TelegramConfig) (*Telegram, error) {
	if strings.TrimSpace(config.BotToken) == "" || strings.TrimSpace(config.ChatID) == "" {
		return nil, fmt.Errorf("для Telegram нужны bot token и chat ID")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.telegram.org"
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	endpoint := strings.TrimRight(config.BaseURL, "/") + "/bot" + url.PathEscape(config.BotToken) + "/sendMessage"
	return &Telegram{
		endpoint: endpoint,
		chatID:   config.ChatID,
		client:   &http.Client{Timeout: config.Timeout},
	}, nil
}

func (t *Telegram) Enabled() bool { return true }

func (t *Telegram) Notify(ctx context.Context, event Event) error {
	if strings.TrimSpace(event.Title) == "" {
		return fmt.Errorf("заголовок уведомления обязателен")
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	payload := struct {
		ChatID              string `json:"chat_id"`
		Text                string `json:"text"`
		DisableNotification bool   `json:"disable_notification"`
	}{
		ChatID: t.chatID,
		Text: fmt.Sprintf(
			"[%s] %s\n%s\nВремя UTC: %s",
			severityLabel(event.Severity),
			event.Title,
			event.Message,
			event.Time.UTC().Format(time.RFC3339),
		),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("сформировать Telegram-запрос: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("не удалось создать Telegram-запрос")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := t.client.Do(request)
	if err != nil {
		return errors.New("Telegram API недоступен")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Telegram вернул HTTP %d", response.StatusCode)
	}
	return nil
}

func severityLabel(severity Severity) string {
	switch severity {
	case SeverityCritical:
		return "КРИТИЧНО"
	case SeverityWarning:
		return "ПРЕДУПРЕЖДЕНИЕ"
	default:
		return "ИНФОРМАЦИЯ"
	}
}
