package paper

import (
	"math"
	"regexp"
	"testing"
)

func TestNormalizeOrderRequest(t *testing.T) {
	request, err := normalizeOrderRequest(SubmitRequest{
		Symbol: " btcusdt ", Side: SideBuy, Quantity: 0.5, MarketPrice: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Symbol != "BTCUSDT" {
		t.Fatalf("символ не нормализован: %q", request.Symbol)
	}
}

func TestNormalizeOrderRejectsNonFiniteValues(t *testing.T) {
	_, err := normalizeOrderRequest(SubmitRequest{
		Symbol: "BTCUSDT", Side: SideBuy, Quantity: math.NaN(), MarketPrice: 60_000,
	})
	if err == nil {
		t.Fatal("NaN quantity должно быть отклонено")
	}
}

func TestGeneratedIDIsUUIDv4(t *testing.T) {
	identifier, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(identifier) {
		t.Fatalf("неожиданный UUID: %q", identifier)
	}
}
