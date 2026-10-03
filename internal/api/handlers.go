package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Pohi11/price-hunter/internal/auth"
	"github.com/Pohi11/price-hunter/internal/products"
)

func identity(r *http.Request) auth.Identity {
	id, _ := auth.FromContext(r.Context()) // guaranteed by auth.Require
	return id
}

func etag(version int) string { return fmt.Sprintf(`W/"%d"`, version) }

// parseIfMatch accepts W/"3", "3" or 3. Returns 0 if absent.
func parseIfMatch(h string) (int, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, true
	}
	h = strings.Trim(strings.TrimPrefix(h, "W/"), `"`)
	n, err := strconv.Atoi(h)
	return n, err == nil && n > 0
}

func (a *API) createProduct(w http.ResponseWriter, r *http.Request) {
	var req createProductRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id := identity(r)
	p, first, err := a.svc.Create(r.Context(), id.UserID, id.Email, products.CreateInput{
		Name: req.Name, URL: req.URL, Target: req.TargetPrice.input(), Frequency: req.CheckFrequency,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/products/"+p.ID)
	w.Header().Set("ETag", etag(p.Version))
	writeJSON(w, http.StatusCreated, toProduct(p, &first, a.opts.Now()))
}

func (a *API) listProducts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, next, err := a.svc.List(r.Context(), identity(r).UserID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	now := a.opts.Now()
	out := listDTO[productDTO]{Items: make([]productDTO, len(items)), NextCursor: next}
	for i, p := range items {
		out.Items[i] = toProduct(p, nil, now)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getProduct(w http.ResponseWriter, r *http.Request) {
	p, latest, err := a.svc.Get(r.Context(), identity(r).UserID, r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(p.Version))
	writeJSON(w, http.StatusOK, toProduct(p, latest, a.opts.Now()))
}

func (a *API) updateProduct(w http.ResponseWriter, r *http.Request) {
	ifMatch, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		badRequest(w, r, "If-Match must be an ETag returned by this API")
		return
	}
	var req updateProductRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := products.UpdateInput{Name: req.Name, Frequency: req.CheckFrequency, Paused: req.Paused, IfMatch: ifMatch}
	if req.TargetPrice != nil {
		m := req.TargetPrice.input()
		in.Target = &m
	}
	p, err := a.svc.Update(r.Context(), identity(r).UserID, r.PathValue("id"), in)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(p.Version))
	writeJSON(w, http.StatusOK, toProduct(p, nil, a.opts.Now()))
}

func (a *API) deleteProduct(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.Delete(r.Context(), identity(r).UserID, r.PathValue("id")); err != nil {
		a.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getPrices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var hq products.HistoryQuery
	for name, dst := range map[string]*time.Time{"from": &hq.From, "to": &hq.To} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				badRequest(w, r, name+" must be an RFC 3339 timestamp")
				return
			}
			*dst = t.UTC()
		}
	}
	hq.Limit, _ = strconv.Atoi(q.Get("limit"))
	hq.Cursor = q.Get("cursor")
	if mp := q.Get("max_points"); mp != "" {
		n, err := strconv.Atoi(mp)
		if err != nil || n < 2 || n > 2000 {
			badRequest(w, r, "max_points must be between 2 and 2000")
			return
		}
		hq.MaxPoints = n
	}
	id := r.PathValue("id")
	h, err := a.svc.Prices(r.Context(), identity(r).UserID, id, hq)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toHistory(id, h))
}

func (a *API) triggerCheck(w http.ResponseWriter, r *http.Request) {
	c, err := a.svc.TriggerCheck(r.Context(), identity(r).UserID, r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	d := toCheck(c)
	w.Header().Set("Location", d.Links["self"])
	writeJSON(w, http.StatusAccepted, d)
}

func (a *API) listChecks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	checks, err := a.svc.ListChecks(r.Context(), identity(r).UserID, r.PathValue("id"), limit)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	out := listDTO[checkDTO]{Items: make([]checkDTO, len(checks))}
	for i, c := range checks {
		out.Items[i] = toCheck(c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getCheck(w http.ResponseWriter, r *http.Request) {
	c, err := a.svc.GetCheck(r.Context(), identity(r).UserID, r.PathValue("id"), r.PathValue("checkId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCheck(c))
}

func (a *API) getMe(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	p, err := a.svc.Me(r.Context(), id.UserID, id.Email)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profileDTO{UserID: p.UserID, Email: p.Email, NotificationsEnabled: p.NotificationsEnabled,
		ProductCount: p.ProductCount, ProductLimit: a.opts.MaxProducts})
}

func (a *API) updateMe(w http.ResponseWriter, r *http.Request) {
	var req updateMeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.NotificationsEnabled == nil {
		badRequest(w, r, "notifications_enabled is required")
		return
	}
	id := identity(r)
	p, err := a.svc.SetNotifications(r.Context(), id.UserID, id.Email, *req.NotificationsEnabled)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profileDTO{UserID: p.UserID, Email: p.Email, NotificationsEnabled: p.NotificationsEnabled,
		ProductCount: p.ProductCount, ProductLimit: a.opts.MaxProducts})
}

func (a *API) listRetailers(w http.ResponseWriter, _ *http.Request) {
	out := retailersDTO{GenericAllowed: a.registry.AllowsGeneric(), Items: []retailerDTO{}}
	for _, p := range a.registry.Profiles() {
		out.Items = append(out.Items, retailerDTO{ID: p.ID, Name: p.Name, Domains: p.Domains, Currency: string(p.Currency)})
	}
	writeJSON(w, http.StatusOK, out)
}
