package server

import (
	"fmt"
	"html"
	"html/template"
	"strings"
	"time"

	"rsc.io/qr"
)

// Template helpers of the account pages. Times are UTC.
var pageFuncs = template.FuncMap{
	"dt": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format("2006-01-02 15:04")
	},
	"day": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format("2006-01-02")
	},
	"dur":   fmtDur,
	"secs":  func(s int64) string { return fmtDur(time.Duration(s) * time.Second) },
	"bytes": fmtBytes,
	// tk translates a dynamic key, falling back to the key itself.
	"tk": func(t map[string]string, prefix, key string) string {
		if v, ok := t[prefix+key]; ok {
			return v
		}
		return key
	},
	"pct": func(a, b int) int {
		if b <= 0 {
			return 0
		}
		return (a*100 + b/2) / b
	},
	"add": func(a, b int) int { return a + b },
	"sub": func(a, b int) int { return a - b },
	"deref": func(p *int64) string {
		if p == nil {
			return ""
		}
		return fmt.Sprint(*p)
	},
	"gb": func(p *int64) string {
		if p == nil {
			return ""
		}
		return fmt.Sprint(*p / 1_000_000_000)
	},
	"tf": func(t map[string]string, key string, args ...any) string { return fmt.Sprintf(t[key], args...) },
}

func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h >= 24:
		return fmt.Sprintf("%dd %02dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1_000_000_000_000:
		return fmt.Sprintf("%.2f TB", float64(n)/1e12)
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2f GB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}

// Chart colors (dark background, no borders): brand red for relayed
// traffic (it costs), a cool tone for direct sessions.
const (
	colDirect = "#4f9dde"
	colRelay  = "#E03A3A"
	colOther  = "#8d8d98"
	colAxis   = "#3a3a44"
	colText   = "#8d8d98"
)

// barSeries is one stacked layer of a bar chart.
type barSeries struct {
	Color  string
	Values []float64
}

// barChart renders a stacked bar chart as inline SVG (no script, no style
// attribute: the CSP allows neither). labels are shown under some bars.
func barChart(title string, labels []string, series []barSeries, fmtMax func(float64) string) template.HTML {
	const w, h, left, bottom, top = 480.0, 200.0, 4.0, 24.0, 20.0
	n := len(labels)
	if n == 0 {
		return ""
	}
	maxV := 0.0
	for i := 0; i < n; i++ {
		sum := 0.0
		for _, s := range series {
			sum += s.Values[i]
		}
		if sum > maxV {
			maxV = sum
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="%s" xmlns="http://www.w3.org/2000/svg">`, w, h, html.EscapeString(title))
	plotH := h - bottom - top
	fmt.Fprintf(&b, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="%s" stroke-width="1"/>`, left, h-bottom, w, h-bottom, colAxis)
	if maxV > 0 {
		fmt.Fprintf(&b, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="%s" stroke-width="1" stroke-dasharray="3 4"/>`, left, top, w, top, colAxis)
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" fill="%s" font-size="14" text-anchor="end">%s</text>`, w, top-6, colText, html.EscapeString(fmtMax(maxV)))
	}
	slot := (w - left) / float64(n)
	bw := slot * 0.68
	every := 1
	if n > 8 {
		every = (n + 5) / 6
	}
	for i := 0; i < n; i++ {
		x := left + float64(i)*slot + (slot-bw)/2
		y := h - bottom
		for _, s := range series {
			v := s.Values[i]
			if v <= 0 || maxV <= 0 {
				continue
			}
			bh := v / maxV * plotH
			if bh < 1 {
				bh = 1
			}
			y -= bh
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="2" fill="%s"/>`, x, y, bw, bh, s.Color)
		}
		if (n-1-i)%every == 0 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s" font-size="14" text-anchor="middle">%s</text>`, min(max(x+bw/2, left+20), w-20), h-5, colText, html.EscapeString(labels[i]))
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) // built from numbers and escaped text only
}

// meter renders a usage bar (value of limit); limit <= 0 draws an empty
// track (unlimited).
func meter(value, limit int64) template.HTML {
	frac := 0.0
	if limit > 0 {
		frac = float64(value) / float64(limit)
		if frac > 1 {
			frac = 1
		}
	}
	col := colDirect
	if frac >= 0.9 {
		col = colRelay
	} else if frac >= 0.7 {
		col = "#e0a03a"
	}
	w := 400 * frac
	return template.HTML(fmt.Sprintf(`<svg class="meter" viewBox="0 0 400 10" preserveAspectRatio="none" xmlns="http://www.w3.org/2000/svg"><rect width="400" height="10" rx="5" fill="#27272e"/><rect width="%.1f" height="10" rx="5" fill="%s"/></svg>`, w, col))
}

// qrSVG renders text as a QR code (inline SVG, black on white, quiet zone).
func qrSVG(text string) (template.HTML, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	size := c.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="qr" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR" xmlns="http://www.w3.org/2000/svg"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, size, size, size, size)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String()), nil
}
