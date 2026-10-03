package notify

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"

	"github.com/Pohi11/price-hunter/internal/domain"
)

type emailData struct {
	Name, URL, Price, Target, Savings, AppURL, SettingsURL string
}

var subjectTmpl = texttemplate.Must(texttemplate.New("subject").Parse(
	`Price alert: {{.Name}} is now {{.Price}}`))

var textTmpl = texttemplate.Must(texttemplate.New("text").Parse(`Good news! {{.Name}} dropped to {{.Price}}, at or below your target of {{.Target}}{{if .Savings}} ({{.Savings}} under target){{end}}.

Buy it here: {{.URL}}

View its price history: {{.AppURL}}

You're getting this because you track this product with Price Hunter.
Turn off alerts: {{.SettingsURL}}
`))

var htmlTmpl = htmltemplate.Must(htmltemplate.New("html").Parse(`<!doctype html>
<html><body style="font-family:system-ui,-apple-system,Segoe UI,sans-serif;color:#1d2433;max-width:520px;margin:0 auto;padding:24px">
<p style="font-size:13px;color:#5b6475;margin:0 0 16px">Price Hunter</p>
<h1 style="font-size:22px;margin:0 0 8px">{{.Name}} is now {{.Price}}</h1>
<p style="margin:0 0 20px">That's at or below your target of <strong>{{.Target}}</strong>{{if .Savings}}, {{.Savings}} under target{{end}}.</p>
<p><a href="{{.URL}}" style="display:inline-block;background:#1f6f43;color:#fff;padding:10px 16px;border-radius:6px;text-decoration:none">View at retailer</a>
&nbsp; <a href="{{.AppURL}}">Price history</a></p>
<p style="font-size:12px;color:#5b6475;margin-top:32px">You're getting this because you track this product with Price Hunter.
<a href="{{.SettingsURL}}">Turn off alerts</a>.</p>
</body></html>`))

// Render builds the alert email for n.
func Render(n domain.Notification, to, baseURL string) (Email, error) {
	base := strings.TrimRight(baseURL, "/")
	d := emailData{
		Name: n.ProductName, URL: n.ProductURL,
		Price: format(n.Price), Target: format(n.Target),
		AppURL: base + "/products/" + n.ProductID, SettingsURL: base + "/settings",
	}
	if n.Target.Minor > n.Price.Minor {
		d.Savings = format(domain.Money{Minor: n.Target.Minor - n.Price.Minor, Currency: n.Price.Currency})
	}
	var subj, text, html bytes.Buffer
	if err := subjectTmpl.Execute(&subj, d); err != nil {
		return Email{}, err
	}
	if err := textTmpl.Execute(&text, d); err != nil {
		return Email{}, err
	}
	if err := htmlTmpl.Execute(&html, d); err != nil {
		return Email{}, err
	}
	subject := strings.ReplaceAll(subj.String(), "\n", " ")
	return Email{To: to, Subject: subject, Text: text.String(), HTML: html.String()}, nil
}

var symbols = map[domain.Currency]string{"USD": "$", "CAD": "CA$", "AUD": "A$", "GBP": "£", "EUR": "€", "JPY": "¥", "INR": "₹"}

// format renders money for people: "$1,299.99".
func format(m domain.Money) string {
	dec := m.Decimal()
	intPart, frac, hasFrac := strings.Cut(dec, ".")
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	s := b.String()
	if hasFrac {
		s += "." + frac
	}
	if sym, ok := symbols[m.Currency]; ok {
		return sym + s
	}
	return fmt.Sprintf("%s %s", s, m.Currency)
}
