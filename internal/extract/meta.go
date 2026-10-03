package extract

import (
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// Meta reads Open Graph product tags and schema.org microdata
// (itemprop="price"). Values in content attributes are machine-formatted;
// visible text is parsed with the human-format parser.
type Meta struct{}

func (Meta) Name() string { return "meta" }

var (
	priceSelectors = []string{
		`meta[property="product:price:amount"]`,
		`meta[property="og:price:amount"]`,
		`[itemprop="price"]`,
	}
	currencySelectors = []string{
		`meta[property="product:price:currency"]`,
		`meta[property="og:price:currency"]`,
		`[itemprop="priceCurrency"]`,
	}
	availabilitySelectors = []string{
		`meta[property="product:availability"]`,
		`meta[property="og:availability"]`,
		`[itemprop="availability"]`,
	}
)

func (Meta) Extract(doc *Document, hint domain.Currency) (Offer, error) {
	cur := hint
	if c := firstValue(doc, currencySelectors); c != "" {
		parsed, err := domain.ParseCurrency(c)
		if err != nil {
			return Offer{}, ErrAmbiguous
		}
		cur = parsed
	}

	for _, sel := range priceSelectors {
		s := doc.Doc.Find(sel).First()
		if s.Length() == 0 {
			continue
		}
		if content, ok := s.Attr("content"); ok && strings.TrimSpace(content) != "" {
			c := cur
			if c == "" {
				c = "USD"
			}
			if m, err := domain.ParseAmount(content, c); err == nil {
				return Offer{Price: m, Availability: availability(doc)}, nil
			}
			m, err := ParsePrice(content, cur)
			if err != nil {
				return Offer{}, err
			}
			return Offer{Price: m, Availability: availability(doc)}, nil
		}
		if text := strings.TrimSpace(s.Text()); text != "" {
			m, err := ParsePrice(text, cur)
			if err != nil {
				return Offer{}, err
			}
			return Offer{Price: m, Availability: availability(doc)}, nil
		}
	}
	return Offer{}, ErrNoPrice
}

func availability(doc *Document) domain.Availability {
	for _, sel := range availabilitySelectors {
		s := doc.Doc.Find(sel).First()
		if s.Length() == 0 {
			continue
		}
		for _, v := range []string{s.AttrOr("content", ""), s.AttrOr("href", ""), s.Text()} {
			if a := ParseAvailability(v); a != domain.AvailabilityUnknown {
				return a
			}
		}
	}
	return domain.AvailabilityUnknown
}

func firstValue(doc *Document, selectors []string) string {
	for _, sel := range selectors {
		s := doc.Doc.Find(sel).First()
		if s.Length() == 0 {
			continue
		}
		v := strings.TrimSpace(s.AttrOr("content", ""))
		if v == "" {
			v = strings.TrimSpace(s.Text())
		}
		if v != "" {
			return v
		}
	}
	return ""
}

// Selectors extracts using CSS selectors from a retailer profile. It is the
// fallback for sites without structured data, and the part most likely to
// break when a site is redesigned.
type Selectors struct {
	Price        []string // tried in order; first match wins
	PriceAttr    string   // read this attribute instead of text, if set
	Title        []string
	Availability *AvailabilityRule
}

// AvailabilityRule maps the text of one element to a stock state.
type AvailabilityRule struct {
	Selector           string
	InStockContains    []string
	OutOfStockContains []string
}

func (Selectors) Name() string { return "selectors" }

func (s Selectors) Extract(doc *Document, hint domain.Currency) (Offer, error) {
	for _, sel := range s.Price {
		el := doc.Doc.Find(sel).First()
		if el.Length() == 0 {
			continue
		}
		raw := el.Text()
		if s.PriceAttr != "" {
			raw = el.AttrOr(s.PriceAttr, "")
		}
		m, err := ParsePrice(raw, hint)
		if err != nil {
			return Offer{}, err
		}
		o := Offer{Price: m, Availability: s.availability(doc)}
		for _, ts := range s.Title {
			if t := strings.TrimSpace(doc.Doc.Find(ts).First().Text()); t != "" {
				o.Title = normalizeSpaces(t)
				break
			}
		}
		return o, nil
	}
	return Offer{}, ErrNoPrice
}

func (s Selectors) availability(doc *Document) domain.Availability {
	if s.Availability == nil {
		return domain.AvailabilityUnknown
	}
	var text string
	doc.Doc.Find(s.Availability.Selector).First().Each(func(_ int, el *goquery.Selection) {
		text = strings.ToLower(normalizeSpaces(el.Text()))
	})
	for _, needle := range s.Availability.OutOfStockContains {
		if strings.Contains(text, strings.ToLower(needle)) {
			return domain.OutOfStock
		}
	}
	for _, needle := range s.Availability.InStockContains {
		if strings.Contains(text, strings.ToLower(needle)) {
			return domain.InStock
		}
	}
	return domain.AvailabilityUnknown
}
