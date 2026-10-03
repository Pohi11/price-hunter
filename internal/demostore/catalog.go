// Package demostore is a fictional retailer used for demos, end-to-end
// tests and failure injection. Prices follow deterministic curves over time,
// so charts look alive and tests are reproducible. Some products
// deliberately misbehave (see Fault).
package demostore

import (
	"math"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// Layout is how a product page exposes its price, one per extraction strategy.
type Layout string

const (
	LayoutJSONLD    Layout = "jsonld"    // schema.org JSON-LD
	LayoutMeta      Layout = "meta"      // Open Graph product tags
	LayoutMicrodata Layout = "microdata" // itemprop attributes
	LayoutSelectors Layout = "selectors" // no structured data; profile CSS selectors only
)

// Fault is a failure mode used to exercise the reliability paths.
type Fault string

const (
	FaultNone        Fault = ""
	FaultFlaky       Fault = "flaky"       // 503 for half of each 10-minute window
	FaultSlow        Fault = "slow"        // responds after 20 s (beyond worker timeouts)
	FaultNoPrice     Fault = "noprice"     // page renders but has no price
	FaultGone        Fault = "gone"        // 410 Gone
	FaultRedesign    Fault = "redesign"    // price markup changes during odd UTC hours
	FaultBlocked     Fault = "blocked"     // 403, as bot protection would
	FaultRateLimited Fault = "ratelimited" // 429 with Retry-After
	FaultGlitch      Fault = "glitch"      // shows a wrong 99% discount 30 min every 12 h
	FaultError       Fault = "error"       // 500 (admin override only)
)

// Product is a demo store item.
type Product struct {
	Slug        string
	Name        string
	Description string
	Base        int64   // minor units (USD cents)
	Amplitude   float64 // relative swing, e.g. 0.2 = +/-20%
	PeriodHours float64
	Phase       float64 // radians
	Layout      Layout
	Fault       Fault
	OOSBelow    float64 // out of stock when the curve's sine is below this (0 = never)
}

// Catalog returns every demo product. Names are fictional on purpose.
func Catalog() []Product {
	return []Product{
		{Slug: "air-fryer", Name: "Turbo Air Fryer 5.5qt", Description: "Crisps with 75% less oil.",
			Base: 11999, Amplitude: 0.22, PeriodHours: 72, Phase: 0, Layout: LayoutJSONLD},
		{Slug: "smartphone-x", Name: "Smartphone X 128GB", Description: "A very rectangular phone.",
			Base: 89900, Amplitude: 0.08, PeriodHours: 240, Phase: 1.3, Layout: LayoutMeta},
		{Slug: "sedan-se", Name: "2026 Midsize Sedan SE", Description: "Dealer price, demo only.",
			Base: 2540000, Amplitude: 0.05, PeriodHours: 500, Phase: 2.1, Layout: LayoutMicrodata},
		{Slug: "espresso-machine", Name: "Barista Pro Espresso Machine", Description: "15 bar pump, steam wand.",
			Base: 49999, Amplitude: 0.15, PeriodHours: 120, Phase: 4.0, Layout: LayoutSelectors},
		{Slug: "anc-headphones", Name: "Quiet Comfort ANC Headphones", Description: "Sells out at its lowest price.",
			Base: 27999, Amplitude: 0.25, PeriodHours: 96, Phase: 0.7, Layout: LayoutJSONLD, OOSBelow: -0.8},
		{Slug: "robot-vacuum", Name: "AutoClean Robot Vacuum", Description: "Maps your living room.",
			Base: 34999, Amplitude: 0.3, PeriodHours: 168, Phase: 3.3, Layout: LayoutJSONLD},

		// Misbehaving products for reliability testing.
		{Slug: "flaky-blender", Name: "Flaky Blender", Description: "Server errors half the time.",
			Base: 7999, Amplitude: 0.1, PeriodHours: 48, Layout: LayoutJSONLD, Fault: FaultFlaky},
		{Slug: "slow-kettle", Name: "Slow Kettle", Description: "Takes 20 seconds to respond.",
			Base: 4999, Amplitude: 0.1, PeriodHours: 48, Layout: LayoutJSONLD, Fault: FaultSlow},
		{Slug: "broken-toaster", Name: "Broken Toaster", Description: "Call for price.",
			Base: 3999, Layout: LayoutJSONLD, Fault: FaultNoPrice},
		{Slug: "discontinued-mixer", Name: "Discontinued Stand Mixer", Description: "No longer sold.",
			Base: 29999, Layout: LayoutJSONLD, Fault: FaultGone},
		{Slug: "redesign-lamp", Name: "Redesign Desk Lamp", Description: "Layout changes every other hour.",
			Base: 5999, Amplitude: 0.1, PeriodHours: 48, Layout: LayoutSelectors, Fault: FaultRedesign},
		{Slug: "blocked-fan", Name: "Blocked Tower Fan", Description: "Always 403.",
			Base: 8999, Layout: LayoutJSONLD, Fault: FaultBlocked},
		{Slug: "ratelimited-heater", Name: "Rate-Limited Heater", Description: "Always 429.",
			Base: 6999, Layout: LayoutJSONLD, Fault: FaultRateLimited},
		{Slug: "glitch-tv", Name: "Glitchy 55\" TV", Description: "Briefly shows a wrong price.",
			Base: 49999, Amplitude: 0.05, PeriodHours: 168, Layout: LayoutJSONLD, Fault: FaultGlitch},
	}
}

// Find returns the product with slug.
func Find(slug string) (Product, bool) {
	for _, p := range Catalog() {
		if p.Slug == slug {
			return p, true
		}
	}
	return Product{}, false
}

// curve returns the sine component at t, in [-1, 1].
func (p Product) curve(t time.Time) float64 {
	if p.PeriodHours == 0 {
		return 0
	}
	hours := float64(t.Unix()) / 3600
	return math.Sin(2*math.Pi*hours/p.PeriodHours + p.Phase)
}

// PriceAt returns the deterministic price of p at time t.
func (p Product) PriceAt(t time.Time) domain.Money {
	if p.Fault == FaultGlitch && glitchWindow(t) {
		return domain.Money{Minor: p.Base / 100, Currency: "USD"} // the "99% off" bug
	}
	v := float64(p.Base) * (1 + p.Amplitude*p.curve(t))
	var minor int64
	if p.Base >= 1_000_000 {
		minor = int64(math.Round(v/10000)) * 10000 // big-ticket: whole $100 steps
	} else {
		minor = int64(v/100)*100 + 99 // retail ".99" pricing
	}
	return domain.Money{Minor: minor, Currency: "USD"}
}

// AvailabilityAt returns stock state at t.
func (p Product) AvailabilityAt(t time.Time) domain.Availability {
	if p.OOSBelow != 0 && p.curve(t) < p.OOSBelow {
		return domain.OutOfStock
	}
	return domain.InStock
}

func glitchWindow(t time.Time) bool {
	t = t.UTC()
	return t.Hour()%12 == 3 && t.Minute() < 30
}
