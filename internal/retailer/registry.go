// Package retailer maps product URLs to retailer profiles and turns a URL
// into a price Quote by combining the safe fetcher with the profile's
// extraction pipeline.
package retailer

import (
	_ "embed"
	"fmt"
	"net"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/extract"
)

//go:embed retailers.yaml
var embeddedProfiles []byte

// GenericID is the profile used for hosts without a dedicated profile.
const GenericID = "generic"

// Profile describes how to extract prices from one retailer.
type Profile struct {
	ID            string          `yaml:"id" json:"id"`
	Name          string          `yaml:"name" json:"name"`
	Domains       []string        `yaml:"domains" json:"domains"`
	Currency      domain.Currency `yaml:"currency" json:"currency,omitempty"`
	Strategies    []string        `yaml:"strategies" json:"strategies"`
	RatePerSecond float64         `yaml:"rate_per_second" json:"-"`
	KeepParams    []string        `yaml:"keep_params" json:"-"`
	Selectors     *selectorConfig `yaml:"selectors" json:"-"`

	pipeline extract.Pipeline
}

type selectorConfig struct {
	Price        []string `yaml:"price"`
	PriceAttr    string   `yaml:"price_attr"`
	Title        []string `yaml:"title"`
	Availability *struct {
		Selector           string   `yaml:"selector"`
		InStockContains    []string `yaml:"in_stock_contains"`
		OutOfStockContains []string `yaml:"out_of_stock_contains"`
	} `yaml:"availability"`
}

// Pipeline returns the profile's extractor chain.
func (p *Profile) Pipeline() extract.Pipeline { return p.pipeline }

type file struct {
	Generic   Profile   `yaml:"generic"`
	Retailers []Profile `yaml:"retailers"`
}

// Options configures a Registry.
type Options struct {
	// AllowGeneric permits hosts without a profile, using structured-data
	// extraction only. Turn it off ("allowlist mode") for a public demo.
	AllowGeneric bool
	// ExtraDomains attaches additional hosts to profiles at runtime,
	// e.g. {"demostore": "abc.lambda-url.us-east-1.on.aws"}.
	ExtraDomains map[string][]string
}

// Registry resolves hosts to profiles. It is immutable after construction.
type Registry struct {
	byDomain map[string]*Profile
	profiles []*Profile
	generic  *Profile
	opts     Options
}

// Default loads the embedded retailers.yaml.
func Default(opts Options) (*Registry, error) { return Load(embeddedProfiles, opts) }

// Load parses a profiles document and builds every pipeline up front, so
// configuration errors fail at startup rather than mid-check.
func Load(data []byte, opts Options) (*Registry, error) {
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse retailer profiles: %w", err)
	}
	r := &Registry{byDomain: map[string]*Profile{}, opts: opts}

	f.Generic.ID = GenericID
	if err := f.Generic.build(); err != nil {
		return nil, err
	}
	r.generic = &f.Generic

	for i := range f.Retailers {
		p := &f.Retailers[i]
		if p.ID == "" || p.ID == GenericID {
			return nil, fmt.Errorf("retailer profile %d: invalid id %q", i, p.ID)
		}
		p.Domains = append(p.Domains, opts.ExtraDomains[p.ID]...)
		if err := p.build(); err != nil {
			return nil, err
		}
		for _, d := range p.Domains {
			d = normalizeHost(d)
			if d == "" {
				continue
			}
			if other, dup := r.byDomain[d]; dup {
				return nil, fmt.Errorf("domain %q claimed by both %s and %s", d, other.ID, p.ID)
			}
			r.byDomain[d] = p
		}
		r.profiles = append(r.profiles, p)
	}
	sort.Slice(r.profiles, func(i, j int) bool { return r.profiles[i].ID < r.profiles[j].ID })
	return r, nil
}

func (p *Profile) build() error {
	if p.Currency != "" {
		c, err := domain.ParseCurrency(string(p.Currency))
		if err != nil {
			return fmt.Errorf("profile %s: %w", p.ID, err)
		}
		p.Currency = c
	}
	if len(p.Strategies) == 0 {
		return fmt.Errorf("profile %s: no strategies", p.ID)
	}
	var exs []extract.Extractor
	for _, s := range p.Strategies {
		switch s {
		case "jsonld":
			exs = append(exs, extract.JSONLD{})
		case "meta":
			exs = append(exs, extract.Meta{})
		case "selectors":
			if p.Selectors == nil || len(p.Selectors.Price) == 0 {
				return fmt.Errorf("profile %s: selectors strategy without price selectors", p.ID)
			}
			sel := extract.Selectors{Price: p.Selectors.Price, PriceAttr: p.Selectors.PriceAttr, Title: p.Selectors.Title}
			if a := p.Selectors.Availability; a != nil {
				sel.Availability = &extract.AvailabilityRule{
					Selector: a.Selector, InStockContains: a.InStockContains, OutOfStockContains: a.OutOfStockContains,
				}
			}
			exs = append(exs, sel)
		default:
			return fmt.Errorf("profile %s: unknown strategy %q", p.ID, s)
		}
	}
	p.pipeline = extract.Pipeline{Extractors: exs}
	return nil
}

// Lookup returns the profile for host (subdomains match their parent
// domain). It returns domain.ErrUnsupportedRetailer when no profile matches
// and generic extraction is disabled.
func (r *Registry) Lookup(host string) (*Profile, error) {
	h := normalizeHost(host)
	for h != "" {
		if p, ok := r.byDomain[h]; ok {
			return p, nil
		}
		_, rest, found := strings.Cut(h, ".")
		if !found {
			break
		}
		h = rest
	}
	if r.opts.AllowGeneric {
		return r.generic, nil
	}
	return nil, domain.ErrUnsupportedRetailer
}

// RateFor returns the request rate for host, for fetch.Options.HostRate.
func (r *Registry) RateFor(host string) float64 {
	p, err := r.Lookup(host)
	if err != nil {
		return 0
	}
	return p.RatePerSecond
}

// byID returns a profile (including generic) by ID, or nil.
func (r *Registry) byID(id string) *Profile {
	if id == GenericID {
		return r.generic
	}
	for _, p := range r.profiles {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Profiles lists dedicated retailer profiles, sorted by ID.
func (r *Registry) Profiles() []*Profile { return r.profiles }

// AllowsGeneric reports whether unknown hosts are accepted.
func (r *Registry) AllowsGeneric() bool { return r.opts.AllowGeneric }

func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(strings.Trim(h, "[]"), ".")
}
