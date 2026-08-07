package shadow

import (
	"reflect"
	"testing"
)

func TestConfigNormalizesAndDeduplicatesSymbols(t *testing.T) {
	config := DefaultConfig()
	config.Symbols = []string{" ethusdt ", "BTCUSDT", "ethusdt"}

	normalized, err := config.normalized()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BTCUSDT", "ETHUSDT"}
	if !reflect.DeepEqual(normalized.Symbols, want) {
		t.Fatalf("неожиданные символы: %#v", normalized.Symbols)
	}
}

func TestConfigRejectsUnsupportedInterval(t *testing.T) {
	config := DefaultConfig()
	config.Interval = "2m"
	if _, err := config.normalized(); err == nil {
		t.Fatal("неподдерживаемый интервал должен быть отклонён")
	}
}
