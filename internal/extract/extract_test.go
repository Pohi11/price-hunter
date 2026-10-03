package extract

import (
	"errors"
	"testing"

	"github.com/Pohi11/price-hunter/internal/domain"
)

func mustDoc(t *testing.T, html string) *Document {
	t.Helper()
	d, err := Parse([]byte(html), "https://shop.example.com/p/1")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestJSONLD(t *testing.T) {
	tests := []struct {
		name  string
		html  string
		minor int64
		cur   domain.Currency
		avail domain.Availability
		title string
	}{
		{
			name: "single product offer",
			html: `<script type="application/ld+json">{"@context":"https://schema.org","@type":"Product","name":"Ninja Air Fryer",
				"offers":{"@type":"Offer","price":"94.00","priceCurrency":"USD","availability":"https://schema.org/InStock"}}</script>`,
			minor: 9400, cur: "USD", avail: domain.InStock, title: "Ninja Air Fryer",
		},
		{
			name: "numeric price and @graph",
			html: `<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebPage"},
				{"@type":["Product"],"name":"Kettle","offers":[{"@type":"Offer","price":39.5,"priceCurrency":"GBP"}]}]}</script>`,
			minor: 3950, cur: "GBP", avail: domain.AvailabilityUnknown, title: "Kettle",
		},
		{
			name: "prefers in-stock offer",
			html: `<script type="application/ld+json">{"@type":"Product","name":"TV","offers":[
				{"@type":"Offer","price":"499.99","priceCurrency":"USD","availability":"OutOfStock"},
				{"@type":"Offer","price":"549.99","priceCurrency":"USD","availability":"InStock"}]}</script>`,
			minor: 54999, cur: "USD", avail: domain.InStock, title: "TV",
		},
		{
			name: "aggregate offer lowPrice",
			html: `<script type="application/ld+json">{"@type":"Product","name":"Phone",
				"offers":{"@type":"AggregateOffer","lowPrice":"899","highPrice":"1099","priceCurrency":"USD"}}</script>`,
			minor: 89900, cur: "USD", avail: domain.AvailabilityUnknown, title: "Phone",
		},
		{
			name: "price specification",
			html: `<script type="application/ld+json">{"@type":"Product","name":"Mixer",
				"offers":{"@type":"Offer","priceSpecification":{"@type":"UnitPriceSpecification","price":"129.00","priceCurrency":"EUR"}}}</script>`,
			minor: 12900, cur: "EUR", avail: domain.AvailabilityUnknown, title: "Mixer",
		},
		{
			name: "car with display-formatted price",
			html: `<script type="application/ld+json">{"@type":"Car","name":"Camry SE",
				"offers":{"@type":"Offer","price":"$25,400","priceCurrency":"USD"}}</script>`,
			minor: 2540000, cur: "USD", avail: domain.AvailabilityUnknown, title: "Camry SE",
		},
		{
			name: "array of top-level nodes with comment wrapper",
			html: `<script type="application/ld+json"><!--[{"@type":"Organization"},{"@type":"Product","name":"Lamp",
				"offers":{"price":"19.99","priceCurrency":"USD","availability":{"@id":"https://schema.org/InStock"}}}]--></script>`,
			minor: 1999, cur: "USD", avail: domain.InStock, title: "Lamp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := JSONLD{}.Extract(mustDoc(t, tt.html), "")
			if err != nil {
				t.Fatal(err)
			}
			if o.Price.Minor != tt.minor || o.Price.Currency != tt.cur || o.Availability != tt.avail || o.Title != tt.title {
				t.Fatalf("got %+v", o)
			}
		})
	}
}

func TestJSONLDFailures(t *testing.T) {
	for name, html := range map[string]string{
		"no scripts":       `<html><body>$94</body></html>`,
		"broken json":      `<script type="application/ld+json">{"@type":"Product",</script>`,
		"not a product":    `<script type="application/ld+json">{"@type":"Organization","name":"Shop"}</script>`,
		"product no offer": `<script type="application/ld+json">{"@type":"Product","name":"X"}</script>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (JSONLD{}).Extract(mustDoc(t, html), "USD"); !errors.Is(err, ErrNoPrice) {
				t.Fatalf("err = %v, want ErrNoPrice", err)
			}
		})
	}
}

func TestMeta(t *testing.T) {
	tests := []struct {
		name  string
		html  string
		minor int64
		cur   domain.Currency
		avail domain.Availability
	}{
		{"open graph", `<meta property="product:price:amount" content="94.00"><meta property="product:price:currency" content="USD">
			<meta property="product:availability" content="in stock">`, 9400, "USD", domain.InStock},
		{"og price", `<meta property="og:price:amount" content="12.5"><meta property="og:price:currency" content="EUR">`, 1250, "EUR", domain.AvailabilityUnknown},
		{"microdata content attr", `<div itemscope itemtype="https://schema.org/Product"><span itemprop="price" content="1299.99">$1,299.99</span>
			<meta itemprop="priceCurrency" content="USD"><link itemprop="availability" href="https://schema.org/OutOfStock"></div>`, 129999, "USD", domain.OutOfStock},
		{"microdata text only", `<span itemprop="price">$1,299.99</span>`, 129999, "USD", domain.AvailabilityUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := Meta{}.Extract(mustDoc(t, tt.html), "")
			if err != nil {
				t.Fatal(err)
			}
			if o.Price.Minor != tt.minor || o.Price.Currency != tt.cur || o.Availability != tt.avail {
				t.Fatalf("got %+v", o)
			}
		})
	}
}

func TestSelectors(t *testing.T) {
	s := Selectors{
		Price: []string{".missing", "p.price_color"},
		Title: []string{"h1"},
		Availability: &AvailabilityRule{
			Selector: "p.availability", InStockContains: []string{"in stock"}, OutOfStockContains: []string{"out of stock"},
		},
	}
	html := `<h1> A Light in the Attic </h1><p class="price_color">£51.77</p>
		<p class="instock availability"><i></i> In stock (22 available) </p>`
	o, err := s.Extract(mustDoc(t, html), "GBP")
	if err != nil {
		t.Fatal(err)
	}
	if o.Price != (domain.Money{Minor: 5177, Currency: "GBP"}) || o.Availability != domain.InStock || o.Title != "A Light in the Attic" {
		t.Fatalf("got %+v", o)
	}

	attr := Selectors{Price: []string{"[data-price]"}, PriceAttr: "data-price"}
	o, err = attr.Extract(mustDoc(t, `<div data-price="19.99">Sale!</div>`), "USD")
	if err != nil || o.Price.Minor != 1999 {
		t.Fatalf("attr extraction: %+v %v", o, err)
	}
}

func TestPipelinePriorityAndDisagreement(t *testing.T) {
	html := `<html><head><title>Fallback title</title><link rel="canonical" href="/p/ninja">
		<script type="application/ld+json">{"@type":"Product","name":"Ninja","offers":{"price":"94.00","priceCurrency":"USD"}}</script>
		<meta property="product:price:amount" content="94.00"><meta property="product:price:currency" content="USD">
		<meta property="product:availability" content="instock"></head>
		<body><span class="promo">$5.00</span></body></html>`
	p := Pipeline{Extractors: []Extractor{JSONLD{}, Meta{}, Selectors{Price: []string{".promo"}}}}
	res, err := p.Run(mustDoc(t, html), "USD")
	if err != nil {
		t.Fatal(err)
	}
	if res.Offer.Strategy != "jsonld" || res.Offer.Price.Minor != 9400 {
		t.Fatalf("winner = %+v", res.Offer)
	}
	if res.Offer.Availability != domain.InStock {
		t.Fatalf("availability not backfilled from meta: %s", res.Offer.Availability)
	}
	if res.Offer.Canonical != "https://shop.example.com/p/ninja" {
		t.Fatalf("canonical = %q", res.Offer.Canonical)
	}
	if !res.Disagreement || len(res.Candidates) != 3 {
		t.Fatalf("expected disagreement across 3 candidates, got %v / %d", res.Disagreement, len(res.Candidates))
	}
}

func TestPipelineErrors(t *testing.T) {
	p := Pipeline{Extractors: []Extractor{JSONLD{}, Meta{}, Selectors{Price: []string{".price"}}}}

	_, err := p.Run(mustDoc(t, `<h1>Nothing here</h1>`), "USD")
	if !errors.Is(err, ErrNoPrice) {
		t.Fatalf("err = %v, want ErrNoPrice", err)
	}
	_, err = p.Run(mustDoc(t, `<span class="price">$10 - $20</span>`), "USD")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("err = %v, want ErrAmbiguous", err)
	}
	_, err = p.Run(mustDoc(t, `<span class="price">$0.00</span>`), "USD")
	if !errors.Is(err, ErrImplausible) {
		t.Fatalf("err = %v, want ErrImplausible", err)
	}
}

func TestParseAvailability(t *testing.T) {
	for in, want := range map[string]domain.Availability{
		"https://schema.org/InStock":      domain.InStock,
		"http://schema.org/OutOfStock":    domain.OutOfStock,
		"schema:SoldOut":                  domain.OutOfStock,
		"in stock":                        domain.InStock,
		"out of stock":                    domain.OutOfStock,
		"LimitedAvailability":             domain.InStock,
		"https://schema.org/Discontinued": domain.OutOfStock,
		"":                                domain.AvailabilityUnknown,
		"maybe":                           domain.AvailabilityUnknown,
	} {
		if got := ParseAvailability(in); got != want {
			t.Errorf("ParseAvailability(%q) = %s, want %s", in, got, want)
		}
	}
}
