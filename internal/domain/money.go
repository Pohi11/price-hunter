// Package domain holds Price Hunter's core types and pure business rules.
// It has no I/O and no dependencies outside the standard library.
package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 currency code, e.g. "USD".
type Currency string

// maxMinor bounds parsed amounts so arithmetic never overflows int64.
const maxMinor = 1_000_000_000_000 // 10 billion in a 2-decimal currency

// exponents lists currencies whose minor-unit exponent differs from 2.
var exponents = map[Currency]int{
	"JPY": 0, "KRW": 0, "VND": 0, "CLP": 0, "ISK": 0,
	"BHD": 3, "JOD": 3, "KWD": 3, "OMR": 3, "TND": 3,
}

// knownCurrencies is the set of codes the parser recognizes in free text.
var knownCurrencies = map[Currency]bool{
	"USD": true, "EUR": true, "GBP": true, "CAD": true, "AUD": true, "NZD": true,
	"JPY": true, "CHF": true, "SEK": true, "NOK": true, "DKK": true, "PLN": true,
	"CZK": true, "HUF": true, "INR": true, "CNY": true, "KRW": true, "MXN": true,
	"BRL": true, "SGD": true, "HKD": true, "ZAR": true, "KWD": true, "BHD": true,
}

// IsKnown reports whether c is a currency code Price Hunter understands.
func (c Currency) IsKnown() bool { return knownCurrencies[c] }

// Exponent returns the number of minor-unit digits (2 for USD, 0 for JPY).
func (c Currency) Exponent() int {
	if e, ok := exponents[c]; ok {
		return e
	}
	return 2
}

// ParseCurrency normalizes and validates an ISO 4217 code.
func ParseCurrency(s string) (Currency, error) {
	c := Currency(strings.ToUpper(strings.TrimSpace(s)))
	if !c.IsKnown() {
		return "", fmt.Errorf("unsupported currency %q", s)
	}
	return c, nil
}

// Money is an amount in integer minor units (cents) plus its currency.
// Floats are never used for prices.
type Money struct {
	Minor    int64    `json:"minor"`
	Currency Currency `json:"currency"`
}

// ErrInvalidAmount is returned when a decimal amount cannot be parsed.
var ErrInvalidAmount = errors.New("invalid amount")

// ParseAmount parses a machine-formatted decimal ("94", "94.5", "1299.99")
// as used in API requests and schema.org structured data. No grouping
// separators or symbols are accepted.
func ParseAmount(s string, c Currency) (Money, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		return Money{}, ErrInvalidAmount
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" || !allDigits(intPart) || (hasDot && (frac == "" || !allDigits(frac))) {
		return Money{}, ErrInvalidAmount
	}
	return fromParts(intPart, frac, c)
}

// fromParts combines integer and fractional digit strings into Money,
// rejecting precision finer than the currency allows unless it is zeros.
func fromParts(intPart, frac string, c Currency) (Money, error) {
	exp := c.Exponent()
	if len(frac) > exp {
		if strings.Trim(frac[exp:], "0") != "" {
			return Money{}, fmt.Errorf("%w: too many decimal places for %s", ErrInvalidAmount, c)
		}
		frac = frac[:exp]
	}
	frac += strings.Repeat("0", exp-len(frac))
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	if len(intPart) > 13 {
		return Money{}, fmt.Errorf("%w: amount too large", ErrInvalidAmount)
	}
	minor, err := strconv.ParseInt(intPart+frac, 10, 64)
	if err != nil || minor > maxMinor {
		return Money{}, fmt.Errorf("%w: amount too large", ErrInvalidAmount)
	}
	return Money{Minor: minor, Currency: c}, nil
}

// NewMoneyFromParts is the exported form of fromParts, used by text parsers
// that have already split a number into integer and fractional digits.
func NewMoneyFromParts(intPart, frac string, c Currency) (Money, error) {
	if !allDigits(intPart) || (frac != "" && !allDigits(frac)) {
		return Money{}, ErrInvalidAmount
	}
	return fromParts(intPart, frac, c)
}

// Decimal renders the amount without a currency, e.g. "94.00".
func (m Money) Decimal() string {
	exp := m.Currency.Exponent()
	s := strconv.FormatInt(m.Minor, 10)
	if exp == 0 {
		return s
	}
	if len(s) <= exp {
		s = strings.Repeat("0", exp-len(s)+1) + s
	}
	return s[:len(s)-exp] + "." + s[len(s)-exp:]
}

// String renders the amount with its currency code, e.g. "94.00 USD".
func (m Money) String() string { return m.Decimal() + " " + string(m.Currency) }

// IsZero reports whether m is the zero value.
func (m Money) IsZero() bool { return m == Money{} }

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
