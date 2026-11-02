package main

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Charts inside Office files (xlsx / pptx / docx): parsed from the cached values in the chart XML,
// described as text (so they can be searched) and drawn as SVG (so they can be shown).

type chartSeries struct {
	Name string
	Vals []float64
}

type chartData struct {
	Type   string // bar | col | line | pie | area | scatter | other
	Title  string
	Cats   []string
	Series []chartSeries
}

var chartEntryRe = regexp.MustCompile(`^(xl|ppt|word)/charts/chart\d+\.xml$`)

func parseChart(b []byte) chartData {
	var cd chartData
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var stack []string
	type ptmap map[int]string
	var cats, vals ptmap
	var curName string
	var inSer bool
	var ptIdx int
	var title strings.Builder
	barDir := "col"
	flush := func() {
		if !inSer {
			return
		}
		n := 0
		for k := range vals {
			if k+1 > n {
				n = k + 1
			}
		}
		s := chartSeries{Name: curName}
		for i := 0; i < n; i++ {
			f, _ := strconv.ParseFloat(strings.TrimSpace(vals[i]), 64)
			s.Vals = append(s.Vals, f)
		}
		if len(cd.Cats) == 0 {
			m := 0
			for k := range cats {
				if k+1 > m {
					m = k + 1
				}
			}
			for i := 0; i < m; i++ {
				cd.Cats = append(cd.Cats, cats[i])
			}
		}
		cd.Series = append(cd.Series, s)
		inSer = false
	}
	has := func(n string) bool {
		for _, s := range stack {
			if s == n {
				return true
			}
		}
		return false
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := t.Name.Local
			stack = append(stack, n)
			switch n {
			case "barChart", "bar3DChart":
				cd.Type = "col"
			case "lineChart", "line3DChart":
				cd.Type = "line"
			case "pieChart", "pie3DChart", "doughnutChart":
				cd.Type = "pie"
			case "areaChart", "area3DChart":
				cd.Type = "line"
			case "scatterChart":
				cd.Type = "scatter"
			case "barDir":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						barDir = a.Value
					}
				}
			case "ser":
				flush()
				inSer, cats, vals, curName = true, ptmap{}, ptmap{}, ""
			case "pt":
				ptIdx = 0
				for _, a := range t.Attr {
					if a.Name.Local == "idx" {
						ptIdx, _ = strconv.Atoi(a.Value)
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "ser" {
				flush()
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			s := string(t)
			switch {
			case has("title") && !has("ser") && has("t") && has("rich"):
				title.WriteString(s)
			case inSer && has("tx") && !has("cat") && !has("val") && (has("v") || has("t")):
				if len(stack) > 0 && stack[len(stack)-1] == "v" {
					curName += s
				}
			case inSer && has("cat") && has("pt") && len(stack) > 0 && stack[len(stack)-1] == "v":
				cats[ptIdx] += s
			case inSer && (has("val") || has("yVal")) && has("pt") && len(stack) > 0 && stack[len(stack)-1] == "v":
				vals[ptIdx] += s
			}
		}
	}
	cd.Title = strings.TrimSpace(title.String())
	if cd.Type == "col" && barDir == "bar" {
		cd.Type = "bar"
	}
	if cd.Type == "" {
		cd.Type = "other"
	}
	return cd
}

func (c chartData) text() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Chart / مخطط / graphique / 图表 (%s)", c.Type)
	if c.Title != "" {
		sb.WriteString(": " + c.Title)
	}
	sb.WriteString(".")
	if len(c.Cats) > 0 {
		sb.WriteString(" Categories: " + strings.Join(c.Cats, ", ") + ".")
	}
	for _, s := range c.Series {
		var v []string
		for i, x := range s.Vals {
			lab := ""
			if i < len(c.Cats) {
				lab = c.Cats[i] + "="
			}
			v = append(v, lab+strconv.FormatFloat(x, 'f', -1, 64))
		}
		sb.WriteString(" " + s.Name + ": " + strings.Join(v, ", ") + ".")
	}
	return sb.String()
}

var chartPalette = []string{"#2563eb", "#f59e0b", "#10b981", "#ef4444", "#8b5cf6", "#06b6d4", "#ec4899", "#84cc16"}

func svgEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func shortLabel(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (c chartData) svg() string {
	const W, H = 680, 380
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" font-family="Arial,sans-serif" font-size="12"><rect width="100%%" height="100%%" fill="#fff"/>`, W, H, W, H)
	if c.Title != "" {
		fmt.Fprintf(&sb, `<text x="%d" y="22" text-anchor="middle" font-size="15" font-weight="bold" fill="#111">%s</text>`, W/2, svgEsc(shortLabel(c.Title, 70)))
	}
	n := len(c.Cats)
	for _, s := range c.Series {
		if len(s.Vals) > n {
			n = len(s.Vals)
		}
	}
	if n == 0 || len(c.Series) == 0 {
		sb.WriteString(`<text x="20" y="60" fill="#666">(no data)</text></svg>`)
		return sb.String()
	}
	cat := func(i int) string {
		if i < len(c.Cats) {
			return c.Cats[i]
		}
		return strconv.Itoa(i + 1)
	}
	// legend
	ly := H - 14
	lx := 20
	for i, s := range c.Series {
		if c.Type == "pie" {
			break
		}
		fmt.Fprintf(&sb, `<rect x="%d" y="%d" width="10" height="10" fill="%s"/><text x="%d" y="%d" fill="#333">%s</text>`, lx, ly-9, chartPalette[i%len(chartPalette)], lx+14, ly, svgEsc(shortLabel(s.Name, 22)))
		lx += 30 + 7*len([]rune(shortLabel(s.Name, 22)))
	}
	if c.Type == "pie" {
		s := c.Series[0]
		var tot float64
		for _, v := range s.Vals {
			if v > 0 {
				tot += v
			}
		}
		if tot == 0 {
			sb.WriteString(`</svg>`)
			return sb.String()
		}
		cx, cy, r := 220.0, 200.0, 130.0
		a0 := -math.Pi / 2
		for i, v := range s.Vals {
			if v <= 0 {
				continue
			}
			a1 := a0 + 2*math.Pi*v/tot
			large := 0
			if a1-a0 > math.Pi {
				large = 1
			}
			fmt.Fprintf(&sb, `<path d="M%.1f %.1f L%.1f %.1f A%.1f %.1f 0 %d 1 %.1f %.1f Z" fill="%s" stroke="#fff"/>`, cx, cy, cx+r*math.Cos(a0), cy+r*math.Sin(a0), r, r, large, cx+r*math.Cos(a1), cy+r*math.Sin(a1), chartPalette[i%len(chartPalette)])
			fmt.Fprintf(&sb, `<rect x="400" y="%d" width="10" height="10" fill="%s"/><text x="416" y="%d" fill="#333">%s — %s (%.0f%%)</text>`, 60+i*20, chartPalette[i%len(chartPalette)], 70+i*20, svgEsc(shortLabel(cat(i), 28)), strconv.FormatFloat(v, 'f', -1, 64), 100*v/tot)
			a0 = a1
		}
		sb.WriteString(`</svg>`)
		return sb.String()
	}
	// axes
	maxV, minV := 0.0, 0.0
	for _, s := range c.Series {
		for _, v := range s.Vals {
			maxV = math.Max(maxV, v)
			minV = math.Min(minV, v)
		}
	}
	if maxV == minV {
		maxV = minV + 1
	}
	step := math.Pow(10, math.Floor(math.Log10(maxV-minV)))
	for (maxV-minV)/step < 4 {
		step /= 2
	}
	for (maxV-minV)/step > 8 {
		step *= 2
	}
	top := math.Ceil(maxV/step) * step
	bot := math.Floor(minV/step) * step
	px0, px1, py0, py1 := 60.0, float64(W-20), 40.0, float64(H-60)
	yv := func(v float64) float64 { return py1 - (v-bot)/(top-bot)*(py1-py0) }
	for g := bot; g <= top+step/2; g += step {
		fmt.Fprintf(&sb, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="#e5e7eb"/><text x="%.0f" y="%.1f" text-anchor="end" fill="#555">%s</text>`, px0, yv(g), px1, yv(g), px0-6, yv(g)+4, strconv.FormatFloat(math.Round(g*100)/100, 'f', -1, 64))
	}
	slot := (px1 - px0) / float64(n)
	for i := 0; i < n; i++ {
		lab := shortLabel(cat(i), 12)
		x := px0 + slot*(float64(i)+0.5)
		if n > 14 && i%((n+13)/14) != 0 {
			continue
		}
		fmt.Fprintf(&sb, `<text x="%.1f" y="%.0f" text-anchor="middle" fill="#333">%s</text>`, x, py1+16, svgEsc(lab))
	}
	ns := len(c.Series)
	for si, s := range c.Series {
		col := chartPalette[si%len(chartPalette)]
		if c.Type == "line" || c.Type == "scatter" {
			var pts []string
			for i, v := range s.Vals {
				pts = append(pts, fmt.Sprintf("%.1f,%.1f", px0+slot*(float64(i)+0.5), yv(v)))
			}
			fmt.Fprintf(&sb, `<polyline fill="none" stroke="%s" stroke-width="2.5" points="%s"/>`, col, strings.Join(pts, " "))
			for _, p := range pts {
				xy := strings.Split(p, ",")
				fmt.Fprintf(&sb, `<circle cx="%s" cy="%s" r="3.5" fill="%s"/>`, xy[0], xy[1], col)
			}
			continue
		}
		bw := slot * 0.7 / float64(ns)
		for i, v := range s.Vals {
			x := px0 + slot*(float64(i)+0.15) + bw*float64(si)
			y0, y1 := yv(math.Max(v, 0)), yv(math.Min(v, 0))
			if c.Type == "bar" { // horizontal bars are drawn as vertical columns for simplicity
			}
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`, x, y0, bw, math.Max(y1-y0, 0.5), col)
			if n <= 12 {
				fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" text-anchor="middle" font-size="10" fill="#333">%s</text>`, x+bw/2, y0-3, strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64))
			}
		}
	}
	fmt.Fprintf(&sb, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="#888"/></svg>`, px0, yv(0), px1, yv(0))
	return sb.String()
}

// drawingMedia returns the picture and chart entries used by a sheet / slide, via its drawing part.
func drawingEntries(zr *zip.ReadCloser, relsPath, baseDir string) (imgs []string, charts []string) {
	seen := map[string]bool{}
	var visit func(rp, bd string, depth int)
	visit = func(rp, bd string, depth int) {
		tg := relTargets(zr, rp, bd)
		var ids []string
		for id := range tg {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			t := tg[id]
			if seen[t] {
				continue
			}
			seen[t] = true
			switch {
			case chartEntryRe.MatchString(t):
				charts = append(charts, t)
			case okImage(zr, t):
				imgs = append(imgs, t)
			case depth == 0 && strings.Contains(t, "/drawings/drawing") && strings.HasSuffix(t, ".xml"):
				visit(path.Join(path.Dir(t), "_rels", path.Base(t)+".rels"), path.Dir(t), 1)
			}
		}
	}
	visit(relsPath, baseDir, 0)
	return
}

func chartMarks(zr *zip.ReadCloser, charts []string) string {
	var sb strings.Builder
	for _, c := range charts {
		b, ok := zipFile(zr, c)
		if !ok {
			continue
		}
		cd := parseChart(b)
		if len(cd.Series) == 0 {
			continue
		}
		sb.WriteString("\n\n" + cd.text() + "\n\n" + imgMark(c))
	}
	return sb.String()
}
