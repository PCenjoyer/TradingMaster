package marketdata

import "testing"

func TestParseTimeSupportsRFC3339AndUnixMilliseconds(t *testing.T) {
	rfc, err := parseTime("2025-01-02T03:04:05Z")
	if err != nil || rfc.Year() != 2025 {
		t.Fatalf("RFC3339 не распознан: %v", err)
	}
	unix, err := parseTime("1735787045000")
	if err != nil || !unix.Equal(rfc) {
		t.Fatalf("Unix milliseconds не распознан: %v, получено %s", err, unix)
	}
}
