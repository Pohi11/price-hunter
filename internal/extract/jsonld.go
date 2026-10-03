package extract

import (
	"encoding/json"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// JSONLD reads schema.org Product/Offer data from <script type="application/ld+json">.
// This is the most reliable strategy: it is machine-readable, used for
// search-engine rich results, and rarely changes when a site is redesigned.
type JSONLD struct{}

func (JSONLD) Name() string { return "jsonld" }

var productTypes = map[string]bool{
	"product": true, "individualproduct": true, "productmodel": true,
	"car": true, "vehicle": true, "book": true,
}

func (JSONLD) Extract(doc *Document, hint domain.Currency) (Offer, error) {
	var products []map[string]any
	doc.Doc.Find(`script`).Each(func(_ int, s *goquery.Selection) {
		if !strings.Contains(strings.ToLower(s.AttrOr("type", "")), "ld+json") {
			return
		}
		for _, node := range decodeLD(s.Text()) {
			collectProducts(node, &products, 0)
		}
	})
	if len(products) == 0 {
		return Offer{}, ErrNoPrice
	}

	var firstErr error
	for _, p := range products {
		offers := offerNodes(p["offers"])
		best, err := pickOffer(offers, hint)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		best.Title = strings.TrimSpace(asString(p["name"]))
		return best, nil
	}
	if firstErr == nil {
		firstErr = ErrNoPrice
	}
	return Offer{}, firstErr
}

// decodeLD parses one script body, tolerating HTML comment wrappers.
func decodeLD(text string) []any {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "<!--")
	text = strings.TrimSuffix(text, "-->")
	text = strings.TrimPrefix(strings.TrimSpace(text), "//<![CDATA[")
	text = strings.TrimSuffix(strings.TrimSpace(text), "//]]>")
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber() // keep "94.00" exact; never round-trip prices through float64
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	if arr, ok := v.([]any); ok {
		return arr
	}
	return []any{v}
}

// collectProducts walks the JSON tree (including @graph) for Product nodes.
func collectProducts(node any, out *[]map[string]any, depth int) {
	if depth > 8 {
		return
	}
	switch n := node.(type) {
	case []any:
		for _, c := range n {
			collectProducts(c, out, depth+1)
		}
	case map[string]any:
		if hasType(n, productTypes) {
			*out = append(*out, n)
			return
		}
		for _, key := range []string{"@graph", "mainEntity", "itemListElement", "item"} {
			if c, ok := n[key]; ok {
				collectProducts(c, out, depth+1)
			}
		}
	}
}

func hasType(n map[string]any, types map[string]bool) bool {
	switch t := n["@type"].(type) {
	case string:
		return types[strings.ToLower(t)]
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && types[strings.ToLower(s)] {
				return true
			}
		}
	}
	return false
}

func offerNodes(v any) []map[string]any {
	var out []map[string]any
	switch o := v.(type) {
	case map[string]any:
		// An AggregateOffer may nest individual offers.
		if inner, ok := o["offers"]; ok && hasType(o, map[string]bool{"aggregateoffer": true}) {
			if nested := offerNodes(inner); len(nested) > 0 {
				out = append(out, nested...)
			}
		}
		out = append(out, o)
	case []any:
		for _, x := range o {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// pickOffer chooses the first in-stock offer with a valid price, else the
// first offer with any valid price.
func pickOffer(offers []map[string]any, hint domain.Currency) (Offer, error) {
	var fallback *Offer
	var firstErr error
	for _, o := range offers {
		price, err := offerPrice(o, hint)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		offer := Offer{Price: price, Availability: ParseAvailability(asString(o["availability"]))}
		if offer.Availability == domain.InStock {
			return offer, nil
		}
		if fallback == nil {
			fallback = &offer
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	if firstErr == nil {
		firstErr = ErrNoPrice
	}
	return Offer{}, firstErr
}

func offerPrice(o map[string]any, hint domain.Currency) (domain.Money, error) {
	currency := asString(o["priceCurrency"])
	raw := asString(o["price"])
	if raw == "" {
		raw = asString(o["lowPrice"])
	}
	if raw == "" {
		for _, spec := range offerNodes(o["priceSpecification"]) {
			if p := asString(spec["price"]); p != "" {
				raw = p
				if currency == "" {
					currency = asString(spec["priceCurrency"])
				}
				break
			}
		}
	}
	if raw == "" {
		return domain.Money{}, ErrNoPrice
	}
	cur := hint
	if currency != "" {
		c, err := domain.ParseCurrency(currency)
		if err != nil {
			return domain.Money{}, ErrAmbiguous
		}
		cur = c
	}
	if cur == "" {
		cur = "USD" // schema.org requires priceCurrency; USD is the sane default for a missing one
	}
	// Spec-compliant values are plain decimals; some sites put display text here.
	if m, err := domain.ParseAmount(raw, cur); err == nil {
		return m, nil
	}
	return ParsePrice(raw, cur)
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case map[string]any:
		// {"@id": "https://schema.org/InStock"} style references
		if id, ok := t["@id"].(string); ok {
			return id
		}
	}
	return ""
}
