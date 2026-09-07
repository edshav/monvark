package util

import "testing"

func TestFormatHashRate(t *testing.T) {
	tests := []struct {
		name string
		rate float64
		want string
	}{
		{"zero", 0, "0 h/s"},
		{"below a kilohash", 999, "999 h/s"},
		{"kilohashes", 1500, "1.50 kh/s"},
		{"gigahashes, the miner's own range", 2.1e9, "2.10 Gh/s"},
		{"beyond the last unit it does not overrun the unit string", 1e24, "1000.00 Zh/s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatHashRate(tt.rate); got != tt.want {
				t.Errorf("FormatHashRate(%v) = %v, want %v", tt.rate, got, tt.want)
			}
		})
	}
}
