package extract

import (
	"errors"
	"testing"

	"github.com/Pohi11/price-hunter/internal/domain"
)

func TestParsePrice(t *testing.T) {
	tests := []struct {
		in    string
		hint  domain.Currency
		minor int64
		cur   domain.Currency
	}{
		{"$94", "", 9400, "USD"},
		{"$94.00", "", 9400, "USD"},
		{"$1,299.99", "", 129999, "USD"},
		{"$25,400", "", 2540000, "USD"},
		{"USD 25,400", "", 2540000, "USD"},
		{"25,400 USD", "", 2540000, "USD"},
		{"US$ 899.00", "", 89900, "USD"},
		{"  $ 94.50  ", "", 9450, "USD"},
		{"$0.99", "", 99, "USD"},
		{"$1,234,567.89", "", 123456789, "USD"},
		{"Price: $94.00", "", 9400, "USD"},
		{"$94.00 each", "", 9400, "USD"},
		{"£51.77", "", 5177, "GBP"},
		{"€ 12,50", "", 1250, "EUR"},
		{"1.299,99 €", "", 129999, "EUR"},
		{"1 299,99 €", "", 129999, "EUR"}, // space grouping
		{"1 299,99 €", "", 129999, "EUR"}, // no-break spaces
		{"1 299,99 €", "", 129999, "EUR"}, // narrow no-break space (fr-FR)
		{"1.299 €", "", 129900, "EUR"},    // single '.' + 3 digits = grouping
		{"CHF 1'299.00", "", 129900, "CHF"},
		{"C$ 49.99", "", 4999, "CAD"},
		{"$49.99", "CAD", 4999, "CAD"}, // bare $ resolves via hint
		{"$49.99", "EUR", 4999, "USD"}, // non-dollar hint doesn't hijack $
		{"49.99", "GBP", 4999, "GBP"},  // no symbol: hint
		{"¥1,500", "", 1500, "JPY"},
		{"₹1,23,456.00", "", 12345600, "INR"}, // Indian grouping
		{"KWD 1.250", "", 1250, "KWD"},        // 3-decimal currency
		{"$94.", "", 9400, "USD"},
		{"$1,29", "", 129, "USD"}, // odd but unambiguous decimal comma
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParsePrice(tt.in, tt.hint)
			if err != nil {
				t.Fatalf("ParsePrice(%q) error: %v", tt.in, err)
			}
			if got.Minor != tt.minor || got.Currency != tt.cur {
				t.Fatalf("ParsePrice(%q) = %d %s, want %d %s", tt.in, got.Minor, got.Currency, tt.minor, tt.cur)
			}
		})
	}
}

func TestParsePriceRejects(t *testing.T) {
	tests := []struct {
		in   string
		hint domain.Currency
		want error
	}{
		{"", "USD", ErrNoPrice},
		{"Call for price", "USD", ErrNoPrice},
		{"$10 - $20", "", ErrAmbiguous},
		{"$10–$20", "", ErrAmbiguous},
		{"Was $129.99 Now $94.00", "", ErrAmbiguous},
		{"4 for $10", "", ErrAmbiguous},
		{"94.00", "", ErrAmbiguous}, // no currency anywhere
		{"$1,2,3", "", ErrAmbiguous},
		{"$12,34,5678", "", ErrAmbiguous},
		{"€1.234,567", "", ErrImplausible}, // finer than cents
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := ParsePrice(tt.in, tt.hint)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ParsePrice(%q) error = %v, want %v", tt.in, err, tt.want)
			}
		})
	}
}

func FuzzParsePrice(f *testing.F) {
	for _, s := range []string{"$1,299.99", "1.299,99 €", "£51.77", "USD 25,400", "₹1,23,456.00", "$10 - $20", "¥1500"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, err := ParsePrice(s, "USD")
		if err != nil {
			if !errors.Is(err, ErrNoPrice) && !errors.Is(err, ErrAmbiguous) && !errors.Is(err, ErrImplausible) {
				t.Fatalf("unexpected error class for %q: %v", s, err)
			}
			return
		}
		if m.Minor < 0 {
			t.Fatalf("negative price from %q", s)
		}
		if !m.Currency.IsKnown() {
			t.Fatalf("unknown currency %q from %q", m.Currency, s)
		}
	})
}
