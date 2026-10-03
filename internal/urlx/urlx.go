// Package urlx validates user-supplied product URLs and canonicalizes them
// for duplicate detection.
//
// Validation here is the *first* SSRF layer: it rejects obviously dangerous
// input early with a helpful error. The authoritative check happens at dial
// time in package fetch, because DNS can change between validation and use.
package urlx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// MaxURLLength bounds stored URLs.
const MaxURLLength = 2048

// Error is a validation failure with a stable, API-facing code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func fail(code, msg string) error { return &Error{Code: code, Message: msg} }

// IsValidationError reports whether err came from Validate.
func IsValidationError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Options adjusts validation. The zero value is the production policy.
type Options struct {
	// AllowLocalhost permits http://localhost:<port> and 127.0.0.1 targets.
	// Only for local development against the demo store; config refuses to
	// enable it outside PH_ENV=local.
	AllowLocalhost bool
}

// Validate parses raw and enforces the URL policy. It returns the parsed URL
// with a lowercased, IDNA-normalized host.
func Validate(raw string, opts Options) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fail("REQUIRED", "URL is required")
	}
	if len(raw) > MaxURLLength {
		return nil, fail("TOO_LONG", "URL must be at most 2048 characters")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" {
		return nil, fail("INVALID_URL", "URL must be an absolute http(s) URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fail("UNSUPPORTED_SCHEME", "only http and https URLs are allowed")
	}
	if u.User != nil {
		return nil, fail("USERINFO_NOT_ALLOWED", "URLs with embedded credentials are not allowed")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return nil, fail("INVALID_URL", "URL must include a host")
	}
	port := u.Port()

	if opts.AllowLocalhost && isLocalhost(host) {
		u.Host = joinHostPort(host, port)
		return u, nil
	}

	if port != "" && port != "80" && port != "443" {
		return nil, fail("PORT_NOT_ALLOWED", "only ports 80 and 443 are allowed")
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return nil, fail("IP_LITERAL_NOT_ALLOWED", "use a domain name, not an IP address")
	}
	if looksNumeric(host) {
		// Catches decimal/octal/hex IPv4 forms like 2130706433 or 0x7f.1 that
		// some resolvers accept.
		return nil, fail("IP_LITERAL_NOT_ALLOWED", "use a domain name, not an IP address")
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return nil, fail("INVALID_HOST", "host name is not valid")
	}
	host = ascii
	suffix, icann := publicsuffix.PublicSuffix(host)
	if !icann || suffix == host || !strings.Contains(host, ".") {
		return nil, fail("INVALID_HOST", "host must be a public domain name")
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".localhost") {
		return nil, fail("INVALID_HOST", "host must be a public domain name")
	}
	u.Host = joinHostPort(host, port)
	return u, nil
}

func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

func joinHostPort(host, port string) string {
	if port == "" {
		return host
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}

// looksNumeric reports whether every dot-separated label is a number in some base.
func looksNumeric(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			continue
		}
		l := strings.TrimPrefix(label, "0x")
		for _, r := range l {
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
			if !isHex {
				return false
			}
		}
		if strings.HasPrefix(label, "0x") {
			continue
		}
		for _, r := range label {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// trackingParams are removed during canonicalization. Matching is by exact
// name or by prefix for entries ending in "*".
var trackingParams = []string{
	"utm_*", "gclid", "gclsrc", "dclid", "gbraid", "wbraid", "fbclid", "msclkid",
	"mc_cid", "mc_eid", "_ga", "_gl", "igshid", "yclid", "srsltid", "ref", "ref_",
	"tag", "irclickid", "irgwc", "clickid", "affid", "cmpid", "spm",
}

func isTracking(name string) bool {
	name = strings.ToLower(name)
	for _, p := range trackingParams {
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
				return true
			}
		} else if name == p {
			return true
		}
	}
	return false
}

// Canonicalize returns a normalized URL string for duplicate detection.
// If keepParams is non-empty, only those query parameters survive; otherwise
// every non-tracking parameter is kept. The result is deterministic: scheme
// and host are lowercased, default ports and fragments removed, and query
// parameters sorted.
func Canonicalize(u *url.URL, keepParams []string) string {
	c := *u
	c.Scheme = strings.ToLower(c.Scheme)
	host := strings.ToLower(c.Hostname())
	port := c.Port()
	if (c.Scheme == "http" && port == "80") || (c.Scheme == "https" && port == "443") {
		port = ""
	}
	c.Host = joinHostPort(host, port)
	c.Fragment = ""
	c.RawFragment = ""
	c.User = nil
	if c.Path == "" {
		c.Path = "/"
	}

	keep := make(map[string]bool, len(keepParams))
	for _, k := range keepParams {
		keep[strings.ToLower(k)] = true
	}
	q := c.Query()
	out := url.Values{}
	for name, vals := range q {
		lname := strings.ToLower(name)
		if len(keep) > 0 {
			if !keep[lname] {
				continue
			}
		} else if isTracking(lname) {
			continue
		}
		sorted := append([]string(nil), vals...)
		sort.Strings(sorted)
		out[name] = sorted
	}
	c.RawQuery = out.Encode() // Encode sorts by key
	return c.String()
}

// StripTracking removes tracking parameters but otherwise keeps the URL as
// the user entered it. This is what we store and fetch.
func StripTracking(u *url.URL) string {
	c := *u
	q := c.Query()
	for name := range q {
		if isTracking(name) {
			q.Del(name)
		}
	}
	c.RawQuery = q.Encode()
	c.Fragment = ""
	c.RawFragment = ""
	return c.String()
}

// Hash returns a stable hex SHA-256 of a canonical URL, used as a uniqueness key.
func Hash(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}
