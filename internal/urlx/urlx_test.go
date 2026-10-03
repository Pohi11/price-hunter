package urlx

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		raw      string
		wantCode string // "" means valid
	}{
		{"https://www.example.com/p/ninja-af101", ""},
		{"http://shop.example.co.uk/item?id=5", ""},
		{"https://EXAMPLE.com:443/x", ""},
		{"https://bücher.de/", ""},
		{"", "REQUIRED"},
		{"not a url", "INVALID_URL"},
		{"/relative/path", "INVALID_URL"},
		{"ftp://example.com/file", "UNSUPPORTED_SCHEME"},
		{"file:///etc/passwd", "UNSUPPORTED_SCHEME"},
		{"gopher://example.com", "UNSUPPORTED_SCHEME"},
		{"javascript:alert(1)", "INVALID_URL"},
		{"https://user:pass@example.com/", "USERINFO_NOT_ALLOWED"},
		{"https://example.com:8080/", "PORT_NOT_ALLOWED"},
		{"https://example.com:22/", "PORT_NOT_ALLOWED"},
		{"http://127.0.0.1/", "IP_LITERAL_NOT_ALLOWED"},
		{"http://169.254.169.254/latest/meta-data/", "IP_LITERAL_NOT_ALLOWED"},
		{"http://[::1]/", "IP_LITERAL_NOT_ALLOWED"},
		{"http://[::ffff:127.0.0.1]/", "IP_LITERAL_NOT_ALLOWED"},
		{"http://2130706433/", "IP_LITERAL_NOT_ALLOWED"},
		{"http://0x7f.1/", "IP_LITERAL_NOT_ALLOWED"},
		{"http://017700000001/", "IP_LITERAL_NOT_ALLOWED"},
		{"http://localhost/", "INVALID_HOST"},
		{"http://localhost:8081/", "PORT_NOT_ALLOWED"},
		{"http://intranet/", "INVALID_HOST"},
		{"http://printer.local/", "INVALID_HOST"},
		{"http://metadata.google.internal/", "INVALID_HOST"},
		{"http://com/", "INVALID_HOST"},
		{"https://" + strings.Repeat("a", 2050) + ".com/", "TOO_LONG"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			_, err := Validate(tt.raw, Options{})
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("Validate(%q) unexpected error: %v", tt.raw, err)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Validate(%q) = %v, want code %s", tt.raw, err, tt.wantCode)
			}
			if e.Code != tt.wantCode {
				t.Fatalf("Validate(%q) code = %s, want %s", tt.raw, e.Code, tt.wantCode)
			}
		})
	}
}

func TestValidateNormalizesHost(t *testing.T) {
	u, err := Validate("https://WWW.Example.COM./x", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "www.example.com" {
		t.Fatalf("host = %q", u.Host)
	}
	u, err = Validate("https://bücher.de/", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "xn--bcher-kva.de" {
		t.Fatalf("IDNA host = %q", u.Host)
	}
}

func TestValidateLocalhostOptIn(t *testing.T) {
	if _, err := Validate("http://localhost:8081/p/air-fryer", Options{AllowLocalhost: true}); err != nil {
		t.Fatalf("local dev opt-in rejected: %v", err)
	}
	// The opt-in is narrow: other private targets stay blocked.
	if _, err := Validate("http://169.254.169.254/", Options{AllowLocalhost: true}); err == nil {
		t.Fatal("metadata IP allowed with AllowLocalhost")
	}
}

func TestCanonicalize(t *testing.T) {
	tests := []struct {
		in   string
		keep []string
		want string
	}{
		{"HTTPS://Example.com:443/p/1?utm_source=x&b=2&a=1#reviews", nil, "https://example.com/p/1?a=1&b=2"},
		{"https://example.com?gclid=abc&fbclid=def", nil, "https://example.com/"},
		{"https://example.com/p?skuId=123&color=red&ref=home", []string{"skuId"}, "https://example.com/p?skuId=123"},
		{"http://example.com:80/x", nil, "http://example.com/x"},
		{"https://example.com/x?b=2&b=1", nil, "https://example.com/x?b=1&b=2"},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := Canonicalize(u, tt.keep); got != tt.want {
			t.Errorf("Canonicalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCanonicalDuplicatesHashEqual(t *testing.T) {
	a, _ := url.Parse("https://shop.example.com/p/ninja?utm_campaign=fall&color=black")
	b, _ := url.Parse("https://SHOP.example.com/p/ninja?color=black#top")
	if Hash(Canonicalize(a, nil)) != Hash(Canonicalize(b, nil)) {
		t.Fatal("equivalent URLs hash differently")
	}
}

func TestStripTracking(t *testing.T) {
	u, _ := url.Parse("https://example.com/p/1?utm_source=news&id=7#frag")
	if got := StripTracking(u); got != "https://example.com/p/1?id=7" {
		t.Fatalf("StripTracking = %q", got)
	}
}

func FuzzValidate(f *testing.F) {
	for _, s := range []string{"https://example.com/", "http://127.0.0.1/", "http://[::1]:80/", "https://a.b.c.example.co.uk/x?y=1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := Validate(raw, Options{})
		if err != nil {
			if !IsValidationError(err) {
				t.Fatalf("non-validation error type %T", err)
			}
			return
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Fatalf("accepted scheme %q", u.Scheme)
		}
		if u.User != nil {
			t.Fatal("accepted userinfo")
		}
		_ = Canonicalize(u, nil) // must not panic
	})
}
