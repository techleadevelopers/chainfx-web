package mobile

import "testing"

func TestDecimalToMinor(t *testing.T) {
	tests := []struct {
		name  string
		value any
		scale int64
		want  int64
	}{
		{name: "brl string", value: "10.25", scale: brlMinorScale, want: 1025},
		{name: "brl comma", value: "10,25", scale: brlMinorScale, want: 1025},
		{name: "brl truncates extra decimals", value: "10.259", scale: brlMinorScale, want: 1025},
		{name: "usdt micros", value: "19.350001", scale: usdtMicroScale, want: 19350001},
		{name: "negative", value: "-1.23", scale: brlMinorScale, want: -123},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decimalToMinor(tt.value, tt.scale); got != tt.want {
				t.Fatalf("decimalToMinor(%v, %d) = %d, want %d", tt.value, tt.scale, got, tt.want)
			}
		})
	}
}

func TestMoneyStrings(t *testing.T) {
	if got := brlMinorString(10490); got != "104.90" {
		t.Fatalf("brlMinorString = %s", got)
	}
	if got := usdtMicroString(19350001); got != "19.350001" {
		t.Fatalf("usdtMicroString = %s", got)
	}
}

func TestFeeMinorRoundsUp(t *testing.T) {
	if got := feeMinor(10000, 490); got != 490 {
		t.Fatalf("feeMinor exact = %d", got)
	}
	if got := feeMinor(9999, 1); got != 1 {
		t.Fatalf("feeMinor minimum positive rounded up = %d", got)
	}
}

func TestUSDTMicrosFromBRLRoundsUp(t *testing.T) {
	if got := usdtMicrosFromBRL(10400, 5200000); got != 20000000 {
		t.Fatalf("usdtMicrosFromBRL exact = %d", got)
	}
	if got := usdtMicrosFromBRL(10000, 5420000); got != 18450185 {
		t.Fatalf("usdtMicrosFromBRL rounded = %d", got)
	}
}
