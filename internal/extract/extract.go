package extract

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// Offer is what a page says about a product's price.
type Offer struct {
	Price        domain.Money
	Availability domain.Availability
	Title        string
	Canonical    string
	Strategy     string // extractor name that produced Price
}

// Document is a parsed page.
type Document struct {
	URL *url.URL
	Doc *goquery.Document
}

// Parse builds a Document from raw HTML.
func Parse(body []byte, pageURL string) (*Document, error) {
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	return &Document{URL: u, Doc: doc}, nil
}

// Extractor is one strategy for finding a price in a document.
// hint is the retailer's default currency (may be empty).
type Extractor interface {
	Name() string
	Extract(doc *Document, hint domain.Currency) (Offer, error)
}

// Result is the outcome of running a Pipeline.
type Result struct {
	Offer        Offer
	Candidates   []Offer // every strategy that found a price, in pipeline order
	Disagreement bool    // strategies found materially different prices
}

// Pipeline runs extractors in priority order. The first successful
// extractor wins; the rest are still evaluated so disagreement between
// strategies (a sign of parsing the wrong element) can be reported.
type Pipeline struct {
	Extractors []Extractor
}

// maxPlausibleMinor rejects absurd values (10 million in a 2-decimal currency).
const maxPlausibleMinor = 1_000_000_000

// Run extracts the best offer from doc.
func (p Pipeline) Run(doc *Document, hint domain.Currency) (Result, error) {
	var res Result
	var errs []error
	for _, ex := range p.Extractors {
		o, err := ex.Extract(doc, hint)
		if err == nil {
			err = plausible(o.Price)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ex.Name(), err))
			continue
		}
		o.Strategy = ex.Name()
		res.Candidates = append(res.Candidates, o)
	}
	if len(res.Candidates) == 0 {
		return res, worstError(errs)
	}
	res.Offer = res.Candidates[0]
	for _, c := range res.Candidates[1:] {
		if disagree(res.Offer.Price, c.Price) {
			res.Disagreement = true
		}
		if res.Offer.Availability == domain.AvailabilityUnknown || res.Offer.Availability == "" {
			res.Offer.Availability = c.Availability
		}
		if res.Offer.Title == "" {
			res.Offer.Title = c.Title
		}
	}
	if res.Offer.Availability == "" {
		res.Offer.Availability = domain.AvailabilityUnknown
	}
	if res.Offer.Title == "" {
		res.Offer.Title = pageTitle(doc)
	}
	if res.Offer.Canonical == "" {
		res.Offer.Canonical = canonicalLink(doc)
	}
	return res, nil
}

func plausible(m domain.Money) error {
	if m.Minor <= 0 {
		return fmt.Errorf("%w: non-positive price %s", ErrImplausible, m)
	}
	if m.Minor > maxPlausibleMinor {
		return fmt.Errorf("%w: price too large %s", ErrImplausible, m)
	}
	return nil
}

// disagree reports a difference of more than 1% or a different currency.
func disagree(a, b domain.Money) bool {
	if a.Currency != b.Currency {
		return true
	}
	diff := a.Minor - b.Minor
	if diff < 0 {
		diff = -diff
	}
	return diff*100 > a.Minor
}

// worstError picks the most informative failure: ambiguity and implausible
// values are more useful to report than a plain "not found".
func worstError(errs []error) error {
	for _, target := range []error{ErrAmbiguous, ErrImplausible} {
		for _, e := range errs {
			if errors.Is(e, target) {
				return e
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w (%w)", ErrNoPrice, errors.Join(errs...))
	}
	return ErrNoPrice
}

func pageTitle(doc *Document) string {
	for _, sel := range []string{`meta[property="og:title"]`, `h1`, `title`} {
		s := doc.Doc.Find(sel).First()
		v := strings.TrimSpace(s.AttrOr("content", ""))
		if v == "" {
			v = strings.TrimSpace(s.Text())
		}
		if v != "" {
			return normalizeSpaces(v)
		}
	}
	return ""
}

func canonicalLink(doc *Document) string {
	href, ok := doc.Doc.Find(`link[rel="canonical"]`).First().Attr("href")
	if !ok || href == "" {
		return ""
	}
	ref, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	return doc.URL.ResolveReference(ref).String()
}

// ParseAvailability maps schema.org / Open Graph availability values.
func ParseAvailability(v string) domain.Availability {
	v = strings.ToLower(strings.TrimSpace(v))
	v = v[strings.LastIndexAny(v, "/:")+1:] // "https://schema.org/InStock" -> "instock"
	v = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(v)
	switch v {
	case "instock", "limitedavailability", "onlineonly", "instoreonly", "preorder", "presale", "backorder", "available":
		return domain.InStock
	case "outofstock", "soldout", "discontinued", "oos", "unavailable":
		return domain.OutOfStock
	}
	return domain.AvailabilityUnknown
}
