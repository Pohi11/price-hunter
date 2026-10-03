package domain

import (
	"errors"
	"testing"
)

func TestParseAmount(t *testing.T) {
	tests := []struct {
		in      string
		cur     Currency
		want    int64
		wantErr bool
	}{
		{"94", "USD", 9400, false},
		{"94.5", "USD", 9450, false},
		{"94.00", "USD", 9400, false},
		{"1299.99", "USD", 129999, false},
		{"0.99", "USD", 99, false},
		{"94.000", "USD", 9400, false}, // extra zeros are fine
		{"94.999", "USD", 0, true},     // finer than cents is rejected
		{"1500", "JPY", 1500, false},
		{"1500.5", "JPY", 0, true},
		{"1.250", "KWD", 1250, false},
		{"", "USD", 0, true},
		{"-5", "USD", 0, true},
		{"+5", "USD", 0, true},
		{"1,299.99", "USD", 0, true}, // machine format has no grouping
		{"$5", "USD", 0, true},
		{"5.", "USD", 0, true},
		{".5", "USD", 0, true},
		{"99999999999999", "USD", 0, true},
		{" 12.30 ", "USD", 1230, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseAmount(tt.in, tt.cur)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseAmount(%q) = %v, want error", tt.in, got)
				}
				if !errors.Is(err, ErrInvalidAmount) {
					t.Fatalf("error %v does not wrap ErrInvalidAmount", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAmount(%q) error: %v", tt.in, err)
			}
			if got.Minor != tt.want || got.Currency != tt.cur {
				t.Fatalf("ParseAmount(%q) = %+v, want %d %s", tt.in, got, tt.want, tt.cur)
			}
		})
	}
}

func TestMoneyDecimal(t *testing.T) {
	tests := []struct {
		m    Money
		want string
	}{
		{Money{9400, "USD"}, "94.00"},
		{Money{5, "USD"}, "0.05"},
		{Money{0, "USD"}, "0.00"},
		{Money{2540000, "USD"}, "25400.00"},
		{Money{1500, "JPY"}, "1500"},
		{Money{1250, "KWD"}, "1.250"},
	}
	for _, tt := range tests {
		if got := tt.m.Decimal(); got != tt.want {
			t.Errorf("%+v.Decimal() = %q, want %q", tt.m, got, tt.want)
		}
	}
}

func TestParseCurrency(t *testing.T) {
	if c, err := ParseCurrency(" usd "); err != nil || c != "USD" {
		t.Fatalf("ParseCurrency(usd) = %q, %v", c, err)
	}
	if _, err := ParseCurrency("XYZ"); err == nil {
		t.Fatal("expected error for unknown currency")
	}
}

func FuzzParseAmount(f *testing.F) {
	for _, s := range []string{"94", "94.50", "0.01", "1e5", "9999999999999.99", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, err := ParseAmount(s, "USD")
		if err != nil {
			return
		}
		if m.Minor < 0 || m.Minor > maxMinor {
			t.Fatalf("ParseAmount(%q) produced out-of-range %d", s, m.Minor)
		}
		// Round trip: rendering then re-parsing yields the same value.
		back, err := ParseAmount(m.Decimal(), "USD")
		if err != nil || back != m {
			t.Fatalf("round trip of %q: %v -> %q -> %v (%v)", s, m, m.Decimal(), back, err)
		}
	})
}
