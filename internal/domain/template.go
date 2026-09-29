package domain

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
)

// helperFuncs is the FuncMap exposed to every template. Kept small — if this
// grows into "every string operation" we should switch to Sprig.
var helperFuncs = template.FuncMap{
	// case
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"title": func(s string) string {
		// Manual title-casing because strings.Title is deprecated.
		if s == "" {
			return s
		}
		parts := strings.Fields(s)
		for i, p := range parts {
			if len(p) == 0 {
				continue
			}
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
		return strings.Join(parts, " ")
	},
	// formatting
	"formatDate": func(v any, layout string) (string, error) {
		t, err := coerceTime(v)
		if err != nil {
			return "", err
		}
		if layout == "" {
			layout = "2006-01-02"
		}
		return t.Format(layout), nil
	},
	"truncate": func(n int, s string) string {
		if n <= 0 || len(s) <= n {
			return s
		}
		return s[:n] + "…"
	},
	"default": func(def, v any) any {
		if v == nil {
			return def
		}
		if s, ok := v.(string); ok && s == "" {
			return def
		}
		return v
	},
}

// coerceTime turns a variety of incoming time representations into a time.Time
// so templates can accept "2026-04-23T10:00:00Z", a native time.Time, or a
// unix-seconds int.
func coerceTime(v any) (time.Time, error) {
	switch x := v.(type) {
	case time.Time:
		return x, nil
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
			if t, err := time.Parse(layout, x); err == nil {
				return t, nil
			}
		}
		return time.Time{}, fmt.Errorf("formatDate: unparseable string %q", x)
	case float64: // JSON numbers land as float64
		return time.Unix(int64(x), 0).UTC(), nil
	case int64:
		return time.Unix(x, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("formatDate: unsupported type %T", v)
}

// Template is a server-stored rendering recipe for notifications. Clients
// reference it by id + variables map on the notification request; the API
// renders at request time so what's enqueued and what's sent to the provider
// are identical (makes idempotency correct — rendered body ends up in the
// SHA-256 hash used by the middleware).
type Template struct {
	ID          uuid.UUID
	Name        string
	Channel     Channel
	Locale      string
	SubjectTmpl *string
	BodyTmpl    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Render executes the stored templates against `vars`. Returns the rendered
// subject (empty for non-email channels) and body. A nil/empty vars map is
// valid; any {{.Field}} that doesn't resolve triggers an ErrInvalidInput.
//
// Helper functions available in every template: upper, lower, title,
// formatDate, truncate, default. See domain.helperFuncs for the full list.
func (t Template) Render(vars map[string]any) (subject, body string, err error) {
	if vars == nil {
		vars = map[string]any{}
	}

	bodyT, err := template.New(t.Name + ":body").
		Option("missingkey=error").
		Funcs(helperFuncs).
		Parse(t.BodyTmpl)
	if err != nil {
		return "", "", fmt.Errorf("%w: template body parse: %s", ErrInvalidInput, err.Error())
	}
	var bodyBuf bytes.Buffer
	if err := bodyT.Execute(&bodyBuf, vars); err != nil {
		return "", "", fmt.Errorf("%w: template body execute: %s", ErrInvalidInput, err.Error())
	}

	if t.SubjectTmpl != nil && *t.SubjectTmpl != "" {
		subjT, err := template.New(t.Name + ":subject").
			Option("missingkey=error").
			Funcs(helperFuncs).
			Parse(*t.SubjectTmpl)
		if err != nil {
			return "", "", fmt.Errorf("%w: template subject parse: %s", ErrInvalidInput, err.Error())
		}
		var subjBuf bytes.Buffer
		if err := subjT.Execute(&subjBuf, vars); err != nil {
			return "", "", fmt.Errorf("%w: template subject execute: %s", ErrInvalidInput, err.Error())
		}
		subject = subjBuf.String()
	}
	return subject, bodyBuf.String(), nil
}
