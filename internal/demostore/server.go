package demostore

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

// Options configures the demo store server.
type Options struct {
	// AdminToken enables /admin endpoints when non-empty. Requests must send
	// "Authorization: Bearer <token>". Locally any token works if set.
	AdminToken string
	// SlowDelay is how long FaultSlow waits (default 20s; tests shorten it).
	SlowDelay time.Duration
	Now       func() time.Time
}

// Server is the demo store's HTTP handler.
type Server struct {
	opts Options
	mux  *http.ServeMux

	mu        sync.RWMutex
	overrides map[string]override
}

type override struct {
	Fault Fault         `json:"fault,omitempty"`
	Price *domain.Money `json:"price,omitempty"`
}

// New builds a Server.
func New(opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SlowDelay == 0 {
		opts.SlowDelay = 20 * time.Second
	}
	s := &Server{opts: opts, mux: http.NewServeMux(), overrides: map[string]override{}}
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("GET /p/{slug}", s.product)
	s.mux.HandleFunc("GET /robots.txt", robots)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /api/products", s.listJSON)
	s.mux.HandleFunc("PUT /admin/products/{slug}", s.admin(s.setOverride))
	s.mux.HandleFunc("DELETE /admin/products/{slug}", s.admin(s.clearOverride))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /admin\n"))
}

// state is a product's effective fault and price at time t, after overrides.
func (s *Server) state(p Product, t time.Time) (Fault, domain.Money) {
	fault, price := p.Fault, p.PriceAt(t)
	s.mu.RLock()
	o, ok := s.overrides[p.Slug]
	s.mu.RUnlock()
	if ok {
		if o.Fault != "" {
			fault = o.Fault
			if fault == "none" {
				fault = FaultNone
			}
		}
		if o.Price != nil {
			price = *o.Price
		}
	}
	return fault, price
}

func (s *Server) product(w http.ResponseWriter, r *http.Request) {
	p, ok := Find(r.PathValue("slug"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := s.opts.Now()
	fault, price := s.state(p, now)

	switch fault {
	case FaultFlaky:
		if now.Minute()%10 < 5 {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
	case FaultSlow:
		select {
		case <-time.After(s.opts.SlowDelay):
		case <-r.Context().Done():
			return
		}
	case FaultGone:
		http.Error(w, "this product has been discontinued", http.StatusGone)
		return
	case FaultBlocked:
		http.Error(w, "access denied", http.StatusForbidden)
		return
	case FaultRateLimited:
		w.Header().Set("Retry-After", "120")
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	case FaultError:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	layout := p.Layout
	showPrice := fault != FaultNoPrice
	if fault == FaultRedesign && now.UTC().Hour()%2 == 1 {
		layout = "redesigned"
	}
	data := pageData{
		Product: p, Layout: string(layout), ShowPrice: showPrice,
		Price: price, Display: display(price), Availability: p.AvailabilityAt(now),
		URL: absURL(r, "/p/"+p.Slug), Now: now.UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := pageTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	now := s.opts.Now()
	var rows []indexRow
	for _, p := range Catalog() {
		fault, price := s.state(p, now)
		rows = append(rows, indexRow{Product: p, Display: display(price), Fault: string(fault), URL: absURL(r, "/p/"+p.Slug)})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, rows); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) listJSON(w http.ResponseWriter, r *http.Request) {
	now := s.opts.Now()
	type item struct {
		Slug  string `json:"slug"`
		Name  string `json:"name"`
		URL   string `json:"url"`
		Price string `json:"price"`
		Fault string `json:"fault,omitempty"`
	}
	var out []item
	for _, p := range Catalog() {
		fault, price := s.state(p, now)
		out = append(out, item{Slug: p.Slug, Name: p.Name, URL: absURL(r, "/p/"+p.Slug), Price: price.Decimal(), Fault: string(fault)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.AdminToken == "" {
			http.Error(w, "admin disabled", http.StatusNotFound)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.AdminToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// setOverride accepts {"fault":"error"} and/or {"price":"79.99"}.
func (s *Server) setOverride(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, ok := Find(slug); !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Fault string `json:"fault"`
		Price string `json:"price"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	o := override{Fault: Fault(body.Fault)}
	if body.Price != "" {
		m, err := domain.ParseAmount(body.Price, "USD")
		if err != nil {
			http.Error(w, "invalid price", http.StatusBadRequest)
			return
		}
		o.Price = &m
	}
	s.mu.Lock()
	s.overrides[slug] = o
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearOverride(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	delete(s.overrides, r.PathValue("slug"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func absURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

// display formats like a US retailer: "$1,299.99".
func display(m domain.Money) string {
	dec := m.Decimal()
	intPart, frac, _ := strings.Cut(dec, ".")
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("$%s.%s", b.String(), frac)
}

type pageData struct {
	Product      Product
	Layout       string
	ShowPrice    bool
	Price        domain.Money
	Display      string
	Availability domain.Availability
	URL          string
	Now          string
}

type indexRow struct {
	Product Product
	Display string
	Fault   string
	URL     string
}

func (d pageData) SchemaAvailability() string {
	if d.Availability == domain.OutOfStock {
		return "https://schema.org/OutOfStock"
	}
	return "https://schema.org/InStock"
}

func (d pageData) StockText() string {
	if d.Availability == domain.OutOfStock {
		return "Out of stock"
	}
	return "In stock"
}

const style = `<style>
body{font-family:system-ui,sans-serif;max-width:760px;margin:40px auto;padding:0 16px;color:#1d2433}
header{display:flex;justify-content:space-between;align-items:baseline;border-bottom:1px solid #ddd;margin-bottom:24px}
.brand{font-weight:700;font-size:20px}.note{color:#777;font-size:13px}
.card{border:1px solid #e3e3e3;border-radius:10px;padding:24px}
.amount,.price-v2,[data-testid=price]{font-size:32px;font-weight:700}
.stock{color:#2a7a2a}.fault{color:#b33;font-size:12px}
table{width:100%;border-collapse:collapse}td{padding:8px;border-bottom:1px solid #eee}
</style>`

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Product.Name}} | Demo Store</title>
<link rel="canonical" href="{{.URL}}">
{{if and .ShowPrice (eq .Layout "meta")}}<meta property="og:type" content="product">
<meta property="og:title" content="{{.Product.Name}}">
<meta property="product:price:amount" content="{{.Price.Decimal}}">
<meta property="product:price:currency" content="USD">
<meta property="product:availability" content="{{if eq .Availability "OUT_OF_STOCK"}}out of stock{{else}}in stock{{end}}">{{end}}
{{if and .ShowPrice (eq .Layout "jsonld")}}<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Product","name":{{.Product.Name}},"description":{{.Product.Description}},
 "sku":{{.Product.Slug}},"offers":{"@type":"Offer","url":{{.URL}},"price":{{.Price.Decimal}},"priceCurrency":"USD",
 "availability":{{.SchemaAvailability}}}}
</script>{{end}}
` + style + `</head>
<body><header><span class="brand">Demo Store</span><span class="note">Fictional retailer for Price Hunter · rendered {{.Now}}</span></header>
{{if eq .Layout "microdata"}}<div class="card" itemscope itemtype="https://schema.org/Product">
<h1 itemprop="name">{{.Product.Name}}</h1><p>{{.Product.Description}}</p>
{{if .ShowPrice}}<div itemprop="offers" itemscope itemtype="https://schema.org/Offer">
<span class="amount" itemprop="price" content="{{.Price.Decimal}}">{{.Display}}</span>
<meta itemprop="priceCurrency" content="USD"><link itemprop="availability" href="{{.SchemaAvailability}}">
<p class="stock">{{.StockText}}</p></div>{{else}}<p>Call for price</p>{{end}}
</div>
{{else if eq .Layout "redesigned"}}<div class="card"><h1>{{.Product.Name}}</h1>
<p>{{.Product.Description}}</p><div class="price-v2">Now only {{.Display}}</div><p>Hurry: offer ends soon, save 20% today!</p></div>
{{else}}<div class="card"><h1>{{.Product.Name}}</h1><p>{{.Product.Description}}</p>
{{if .ShowPrice}}<div class="product-price"><span data-testid="price">{{.Display}}</span></div>
<p class="stock">{{.StockText}}</p>{{else}}<p>Call for price</p>{{end}}</div>{{end}}
</body></html>`))

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Demo Store</title>` + style + `</head>
<body><header><span class="brand">Demo Store</span><span class="note">Fictional retailer for Price Hunter</span></header>
<table>{{range .}}<tr><td><a href="{{.URL}}">{{.Product.Name}}</a>{{if .Fault}} <span class="fault">[{{.Fault}}]</span>{{end}}</td>
<td>{{.Display}}</td><td class="note">{{.Product.Layout}}</td></tr>{{end}}</table></body></html>`))
