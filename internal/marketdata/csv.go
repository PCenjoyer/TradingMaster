package marketdata

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/PCenjoyer/TradingMaster/internal/domain"
)

var requiredColumns = []string{"timestamp", "open", "high", "low", "close", "volume"}

func ReadCSV(path string) ([]domain.Candle, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("открыть CSV: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.ReuseRecord = true
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("прочитать заголовок CSV: %w", err)
	}
	indexes := make(map[string]int, len(header))
	for index, name := range header {
		indexes[strings.ToLower(strings.TrimSpace(name))] = index
	}
	for _, name := range requiredColumns {
		if _, ok := indexes[name]; !ok {
			return nil, fmt.Errorf("в CSV отсутствует колонка %q", name)
		}
	}

	candles := make([]domain.Candle, 0, 1024)
	for line := 2; ; line++ {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("строка %d: %w", line, readErr)
		}
		candle, parseErr := parseRecord(record, indexes)
		if parseErr != nil {
			return nil, fmt.Errorf("строка %d: %w", line, parseErr)
		}
		candles = append(candles, candle)
	}
	return candles, nil
}

func parseRecord(record []string, indexes map[string]int) (domain.Candle, error) {
	value := func(name string) (string, error) {
		index := indexes[name]
		if index >= len(record) {
			return "", fmt.Errorf("нет значения %q", name)
		}
		return strings.TrimSpace(record[index]), nil
	}
	parseFloat := func(name string) (float64, error) {
		raw, err := value(name)
		if err != nil {
			return 0, err
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return parsed, nil
	}

	rawTime, err := value("timestamp")
	if err != nil {
		return domain.Candle{}, err
	}
	timestamp, err := parseTime(rawTime)
	if err != nil {
		return domain.Candle{}, err
	}
	open, err := parseFloat("open")
	if err != nil {
		return domain.Candle{}, err
	}
	high, err := parseFloat("high")
	if err != nil {
		return domain.Candle{}, err
	}
	low, err := parseFloat("low")
	if err != nil {
		return domain.Candle{}, err
	}
	closePrice, err := parseFloat("close")
	if err != nil {
		return domain.Candle{}, err
	}
	volume, err := parseFloat("volume")
	if err != nil {
		return domain.Candle{}, err
	}
	return domain.Candle{Time: timestamp, Open: open, High: high, Low: low, Close: closePrice, Volume: volume}, nil
}

func parseTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	unix, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp должен быть RFC3339 либо Unix: %w", err)
	}
	if unix > 10_000_000_000 {
		unix /= 1_000
	}
	return time.Unix(unix, 0).UTC(), nil
}
