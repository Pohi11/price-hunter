// Package extract turns retailer HTML into an Offer (price, currency,
// availability). Extraction is a chain of strategies, most reliable first:
// schema.org JSON-LD, then meta tags / microdata, then per-retailer CSS
// selectors from configuration.
package extract

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// Extraction errors. Checkers map these to outcome codes.
var (
	ErrNoPrice     = errors.New("no price found")
	ErrAmbiguous   = errors.New("ambiguous price")
	ErrImplausible = errors.New("implausible price")
)

// symbols maps currency symbols and prefixes to codes. Longer prefixes are
// matched first. A bare "$" resolves via the currency hint.
var symbols = []struct {
	sym string
	cur domain.Currency
}{
	{"US$", "USD"}, {"CA$", "CAD"}, {"C$", "CAD"}, {"AU$", "AUD"}, {"A$", "AUD"},
	{"NZ$", "NZD"}, {"HK$", "HKD"}, {"S$", "SGD"}, {"R$", "BRL"}, {"MX$", "MXN"},
	{"€", "EUR"}, {"£", "GBP"}, {"¥", "JPY"}, {"₹", "INR"}, {"₩", "KRW"}, {"zł", "PLN"},
	{"Kč", "CZK"},
}

var dollarCurrencies = map[domain.Currency]bool{
	"USD": true, "CAD": true, "AUD": true, "NZD": true, "HKD": true, "SGD": true, "MXN": true,
}

// numberRe matches a number with optional grouping/decimal separators.
// Spaces (incl. thin/no-break spaces normalized to ' ') and apostrophes may
// group thousands, as in "1 299,99" or "1'299.00".
var numberRe = regexp.MustCompile(`\d(?:[\d.,']|\s\d)*`)

var codeRe = regexp.MustCompile(`\b[A-Z]{3}\b`)

// ParsePrice parses human-formatted price text such as "$1,299.99",
// "1.299,99 €", "USD 25,400" or "£51.77". hint is used when the text has no
// currency or only an ambiguous symbol like "$"; it may be empty.
//
// Text containing more than one number ("$10 - $20", "Was $129 now $94")
// is rejected as ambiguous rather than guessed.
func ParsePrice(text string, hint domain.Currency) (domain.Money, error) {
	s := normalizeSpaces(text)
	if s == "" {
		return domain.Money{}, ErrNoPrice
	}
	cur, err := detectCurrency(s, hint)
	if err != nil {
		return domain.Money{}, err
	}

	nums := numberRe.FindAllString(s, -1)
	switch len(nums) {
	case 0:
		return domain.Money{}, ErrNoPrice
	case 1:
	default:
		return domain.Money{}, fmt.Errorf("%w: %d numbers in %q", ErrAmbiguous, len(nums), truncate(s))
	}
	intPart, frac, err := splitNumber(strings.TrimSpace(nums[0]), cur.Exponent())
	if err != nil {
		return domain.Money{}, err
	}
	m, err := domain.NewMoneyFromParts(intPart, frac, cur)
	if err != nil {
		return domain.Money{}, fmt.Errorf("%w: %w", ErrImplausible, err)
	}
	return m, nil
}

func detectCurrency(s string, hint domain.Currency) (domain.Currency, error) {
	upper := strings.ToUpper(s)
	for _, code := range codeRe.FindAllString(upper, -1) {
		if c := domain.Currency(code); c.IsKnown() {
			return c, nil
		}
	}
	for _, sym := range symbols {
		if strings.Contains(s, sym.sym) {
			return sym.cur, nil
		}
	}
	if strings.Contains(s, "$") {
		if dollarCurrencies[hint] {
			return hint, nil
		}
		return "USD", nil
	}
	if hint != "" {
		return hint, nil
	}
	return "", fmt.Errorf("%w: no currency in %q", ErrAmbiguous, truncate(s))
}

// splitNumber decides which separator is the decimal point and returns the
// integer and fractional digit strings.
//
// Rules, in order:
//   - both '.' and ',' present: the last one is the decimal separator
//   - one kind present more than once: it is a grouping separator
//   - one occurrence followed by exactly 3 digits: grouping ("1,299" or "1.299")
//     unless the currency has 3 decimal places
//   - otherwise: decimal separator ("94,99", "94.5")
func splitNumber(n string, exponent int) (string, string, error) {
	n = strings.NewReplacer(" ", "", "'", "").Replace(n)
	n = strings.TrimRight(n, ".,")
	lastDot := strings.LastIndex(n, ".")
	lastComma := strings.LastIndex(n, ",")

	decimalAt := -1
	switch {
	case lastDot >= 0 && lastComma >= 0:
		decimalAt = max(lastDot, lastComma)
	case lastDot >= 0 || lastComma >= 0:
		sep := "."
		pos := lastDot
		if lastComma >= 0 {
			sep, pos = ",", lastComma
		}
		after := len(n) - pos - 1
		switch {
		case strings.Count(n, sep) > 1:
			decimalAt = -1
		case after == 3 && exponent != 3:
			decimalAt = -1
		default:
			decimalAt = pos
		}
	}

	intPart, frac := n, ""
	if decimalAt >= 0 {
		intPart, frac = n[:decimalAt], n[decimalAt+1:]
	}
	if err := validGrouping(intPart); err != nil {
		return "", "", err
	}
	intPart = strings.NewReplacer(".", "", ",", "").Replace(intPart)
	if intPart == "" {
		intPart = "0"
	}
	if strings.ContainsAny(frac, ".,") {
		return "", "", fmt.Errorf("%w: malformed number %q", ErrAmbiguous, n)
	}
	return intPart, frac, nil
}

// validGrouping checks digit grouping: the first group has 1-3 digits, the
// last exactly 3, and middle groups 2 or 3 (allowing Indian "1,23,456").
func validGrouping(intPart string) error {
	groups := strings.Split(strings.NewReplacer(",", ".").Replace(intPart), ".")
	if len(groups) <= 1 {
		return nil
	}
	bad := fmt.Errorf("%w: malformed grouping %q", ErrAmbiguous, intPart)
	if len(groups[0]) == 0 || len(groups[0]) > 3 || len(groups[len(groups)-1]) != 3 {
		return bad
	}
	for _, g := range groups[1 : len(groups)-1] {
		if len(g) != 2 && len(g) != 3 {
			return bad
		}
	}
	return nil
}

func normalizeSpaces(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', ' ', ' ', ' ', '\t', '\n', '\r':
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
