package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"
)

// POB: arrivals / departures archive, professional export (xlsx + printable page).

type Stay struct {
	In   string `json:"in"`            // YYYY-MM-DDTHH:MM
	Out  string `json:"out,omitempty"` // empty = still on board
	Note string `json:"note,omitempty"`
}

func normShift(s string) string {
	switch lc(s) {
	case "day", "jour", "نهار", "النهار", "نهاري", "d":
		return "Day"
	case "night", "nuit", "ليل", "الليل", "ليلي", "n":
		return "Night"
	}
	return ""
}

// normWhen accepts 2026-10-10, 2026-10-10 14:30, 2026-10-10T14:30, 10/10/2026 14:30 -> YYYY-MM-DDTHH:MM.
func normWhen(s string) string {
	s = strings.TrimSpace(strings.Replace(s, "T", " ", 1))
	if s == "" {
		return ""
	}
	for _, l := range []string{"2006-01-02 15:04", "2006-01-02", "02/01/2006 15:04", "02/01/2006", "02-01-2006 15:04", "02-01-2006"} {
		if t, err := time.Parse(l, s); err == nil {
			if !strings.Contains(l, "15") {
				return t.Format("2006-01-02") + "T00:00"
			}
			return t.Format("2006-01-02T15:04")
		}
	}
	return ""
}

func (p Person) onBoardNow() bool {
	if len(p.Stays) == 0 {
		return true // old records: always on board
	}
	return p.Stays[len(p.Stays)-1].Out == ""
}

// onBoardBetween: was the person on board at any moment of [from, to] (YYYY-MM-DDTHH:MM strings)?
func (p Person) onBoardBetween(from, to string) bool {
	if len(p.Stays) == 0 {
		return true
	}
	for _, s := range p.Stays {
		if (s.In == "" || s.In <= to) && (s.Out == "" || s.Out >= from) {
			return true
		}
	}
	return false
}

func dayRange(d string) (string, string) { return d + "T00:00", d + "T23:59" }

func (h *hubStore) move(ids []string, typ, when, note string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	w := normWhen(when)
	if w == "" {
		w = time.Now().Format("2006-01-02T15:04")
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	n := 0
	for i := range h.h.People {
		p := &h.h.People[i]
		if !want[p.ID] {
			continue
		}
		if len(p.Stays) == 0 { // legacy record: it was on board since it was added
			p.Stays = []Stay{{In: p.Added + "T00:00"}}
		}
		last := &p.Stays[len(p.Stays)-1]
		switch typ {
		case "out":
			if last.Out == "" {
				last.Out, last.Note = w, note
				n++
			}
		case "in":
			if last.Out != "" {
				p.Stays = append(p.Stays, Stay{In: w, Note: note})
				n++
			}
		}
	}
	_ = h.saveLocked()
	return n
}

// peopleOnBoard returns the people of the given companies that were on board during [from,to] (all when from=="").
func (h *hubStore) peopleOnBoard(from, to string) []Person {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Person
	for _, p := range h.h.People {
		if from == "" {
			if p.onBoardNow() {
				out = append(out, p)
			}
		} else if p.onBoardBetween(from, to) {
			out = append(out, p)
		}
	}
	return out
}

type pobReq struct {
	Date      string   `json:"date"` // YYYY-MM-DD ("" = now)
	Companies []string `json:"companies"`
	Rig       string   `json:"rig"`
	Well      string   `json:"well"`
	Phone     bool     `json:"phone"`
	Shift     bool     `json:"shift"`
	Photo     bool     `json:"photo"`
}

func stripLabel(s, label string) string {
	s = strings.TrimSpace(s)
	if len(s) >= len(label) && strings.EqualFold(s[:len(label)], label) {
		s = strings.TrimSpace(strings.TrimLeft(s[len(label):], ": "))
	}
	return s
}

func (a *app) pobPeople(rq pobReq) []Person {
	var from, to string
	if rq.Date != "" {
		from, to = dayRange(rq.Date)
	}
	list := a.hub.peopleOnBoard(from, to)
	want := map[string]bool{}
	for _, c := range rq.Companies {
		want[lc(c)] = true
	}
	var out []Person
	for _, p := range list {
		if len(want) == 0 || want[lc(p.Company)] {
			out = append(out, p)
		}
	}
	return out
}

type pobGroup struct {
	Company string
	People  []Person
}

func groupByCompany(people []Person) []pobGroup {
	m := map[string]*pobGroup{}
	var order []string
	for _, p := range people {
		c := p.Company
		if c == "" {
			c = "—"
		}
		if m[c] == nil {
			m[c] = &pobGroup{Company: c}
			order = append(order, c)
		}
		m[c].People = append(m[c].People, p)
	}
	// the main company first (most people), others alphabetical
	sort.SliceStable(order, func(i, j int) bool {
		a, b := len(m[order[i]].People), len(m[order[j]].People)
		if a != b {
			return a > b
		}
		return order[i] < order[j]
	})
	var out []pobGroup
	for _, c := range order {
		out = append(out, *m[c])
	}
	return out
}

func frDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		t = time.Now()
	}
	mo := []string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}
	return fmt.Sprintf("%d %s %d", t.Day(), mo[t.Month()-1], t.Year())
}

func buildPOBXlsx(groups []pobGroup, rq pobReq) []byte {
	rq.Rig, rq.Well = stripLabel(rq.Rig, "RIG"), stripLabel(rq.Well, "WELL")
	date := rq.Date
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	total := 0
	for _, g := range groups {
		total += len(g.People)
	}
	sh := XSheet{Name: "POB", Fit: true, Widths: map[int]int{0: 5, 1: 34, 2: 26, 3: 18, 4: 9}}
	ncol := 5
	extra := []string{}
	if rq.Phone {
		extra = append(extra, "TÉLÉPHONE")
	}
	if rq.Shift {
		extra = append(extra, "ÉQUIPE")
	}
	for i := range extra {
		sh.Widths[5+i] = 16
	}
	ncol += len(extra)
	row := func(m map[int]XCell) {
		mx := 0
		for c := range m {
			if c > mx {
				mx = c
			}
		}
		r := make([]XCell, mx+1)
		for c, v := range m {
			r[c] = v
		}
		sh.Rows = append(sh.Rows, r)
	}
	row(nil)
	row(map[int]XCell{1: xt("RIG: "+rq.Rig, 6)})
	row(map[int]XCell{1: xt("WELL: "+rq.Well, 6), 3: xt(frDate(date), 6)})
	row(map[int]XCell{0: xt("Liste du personnel on bord (POB)", 3), 3: xt("", 0)})
	totRow := len(sh.Rows)
	row(nil)
	hdr := map[int]XCell{0: xt("N°", 7), 1: xt("Nom Et Prénom", 7), 2: xt("Fonction", 7), 3: xt("SOCIETE", 7), 4: xt("NOMBRE", 7)}
	for i, e := range extra {
		hdr[5+i] = xt(e, 7)
	}
	row(hdr)
	hdrRow := len(sh.Rows)
	var totalCells []string
	for _, g := range groups {
		band := map[int]XCell{}
		for c := 0; c < ncol; c++ {
			band[c] = xt("", 8)
		}
		band[0] = xt(g.Company, 8)
		row(band)
		sh.Merges = append(sh.Merges, fmt.Sprintf("A%d:%s%d", len(sh.Rows), colName(ncol-1), len(sh.Rows)))
		first := len(sh.Rows) + 1
		for i, p := range g.People {
			r := map[int]XCell{0: xt(fmt.Sprint(i+1), 4), 1: xt(p.Name, 2), 2: xt(p.Function, 4), 3: xt(g.Company, 4), 4: xt("1", 4)}
			k := 5
			if rq.Phone {
				r[k] = xt(p.Phone, 4)
				k++
			}
			if rq.Shift {
				r[k] = xt(map[string]string{"Day": "Jour", "Night": "Nuit"}[p.Shift], 4)
			}
			row(r)
		}
		last := len(sh.Rows)
		t := map[int]XCell{}
		for c := 0; c < ncol; c++ {
			t[c] = xt("", 9)
		}
		t[0] = xt("TOTAL", 9)
		t[4] = xf(fmt.Sprintf("SUM(E%d:E%d)", first, last), 9)
		row(t)
		sh.Merges = append(sh.Merges, fmt.Sprintf("A%d:D%d", len(sh.Rows), len(sh.Rows)))
		totalCells = append(totalCells, fmt.Sprintf("E%d", len(sh.Rows)))
		row(nil)
	}
	g := map[int]XCell{3: xt("TOTAL POB", 9), 4: xf("SUM("+strings.Join(totalCells, ",")+")", 9)}
	row(g)
	// the title row shows the grand total as in the original model
	sh.Rows[totRow-1][3] = xf(`"Total : "&`+strings.TrimPrefix(fmt.Sprintf("E%d", len(sh.Rows)), ""), 6)
	sh.Merges = append(sh.Merges, fmt.Sprintf("A%d:C%d", totRow, totRow))
	sh.PrintTitles = fmt.Sprintf("%d:%d", hdrRow, hdrRow)
	// summary sheet by company with a chart
	sum := XSheet{Name: "Summary", Widths: map[int]int{0: 30, 1: 12}}
	sum.Rows = append(sum.Rows, []XCell{xt("POB "+frDate(date), 3)}, nil, []XCell{xt("Société", 1), xt("Effectif", 1)})
	for _, gr := range groups {
		sum.Rows = append(sum.Rows, []XCell{xt(gr.Company, 2), xt(fmt.Sprint(len(gr.People)), 4)})
	}
	n := len(groups)
	sum.Rows = append(sum.Rows, []XCell{xt("TOTAL", 9), xf(fmt.Sprintf("SUM(B4:B%d)", 3+n), 9)})
	if n > 0 {
		sum.Charts = append(sum.Charts, XChart{Type: "col", Title: "POB / société", Cats: fmt.Sprintf("A4:A%d", 3+n), Series: []XSeries{{Name: "Effectif", Ref: fmt.Sprintf("B4:B%d", 3+n)}}, From: [2]int{3, 2}, To: [2]int{11, 18}})
	}
	_ = total
	return buildBook([]XSheet{sh, sum})
}

func pobPrintHTML(groups []pobGroup, rq pobReq) string {
	rq.Rig, rq.Well = stripLabel(rq.Rig, "RIG"), stripLabel(rq.Well, "WELL")
	date := rq.Date
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	total := 0
	for _, g := range groups {
		total += len(g.People)
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>POB ` + html.EscapeString(date) + `</title><style>
@page{size:A4;margin:12mm}body{font-family:Arial,sans-serif;font-size:11px;color:#000;margin:0}
.top{display:flex;justify-content:space-between;align-items:flex-end;margin-bottom:6px}h1{font-size:15px;margin:4px 0}
table{width:100%;border-collapse:collapse;margin-bottom:10px;page-break-inside:auto}tr{page-break-inside:avoid}
th,td{border:1px solid #444;padding:2px 5px;text-align:center}td.l{text-align:left}th{background:#d9d9d9}
.band td{background:#92d050;font-weight:bold}.tot td{background:#00b0f0;font-weight:bold}
img.ph{width:26px;height:26px;object-fit:cover;border-radius:3px;display:block;margin:auto}
@media screen{body{padding:14px}.noprint{background:#fffbe6;border:1px solid #e6c200;padding:6px;margin-bottom:10px}}@media print{.noprint{display:none}}
</style></head><body><div class="noprint">للحفظ PDF: اضغط Ctrl+P ثم «حفظ كـ PDF».</div>`)
	fmt.Fprintf(&b, `<div class="top"><div><b>RIG: %s</b><br><b>WELL: %s</b><h1>Liste du personnel on bord (POB)</h1></div><div style="text-align:right"><b>%s</b><br>Total : <b>%d</b></div></div>`, html.EscapeString(rq.Rig), html.EscapeString(rq.Well), html.EscapeString(frDate(date)), total)
	for _, g := range groups {
		b.WriteString(`<table><thead><tr><th style="width:4%">N°</th>`)
		if rq.Photo {
			b.WriteString(`<th style="width:5%">Photo</th>`)
		}
		b.WriteString(`<th>Nom Et Prénom</th><th>Fonction</th><th>SOCIETE</th><th style="width:7%">NOMBRE</th>`)
		if rq.Phone {
			b.WriteString(`<th>TÉLÉPHONE</th>`)
		}
		if rq.Shift {
			b.WriteString(`<th>ÉQUIPE</th>`)
		}
		cols := 5 + map[bool]int{true: 1}[rq.Photo] + map[bool]int{true: 1}[rq.Phone] + map[bool]int{true: 1}[rq.Shift]
		fmt.Fprintf(&b, `</tr></thead><tbody><tr class="band"><td colspan="%d">%s</td></tr>`, cols, html.EscapeString(g.Company))
		for i, p := range g.People {
			fmt.Fprintf(&b, `<tr><td>%d</td>`, i+1)
			if rq.Photo {
				if p.Photo != "" {
					fmt.Fprintf(&b, `<td><img class="ph" src="%s"></td>`, html.EscapeString(p.Photo))
				} else {
					b.WriteString(`<td></td>`)
				}
			}
			fmt.Fprintf(&b, `<td class="l">%s</td><td>%s</td><td><b>%s</b></td><td>1</td>`, html.EscapeString(p.Name), html.EscapeString(p.Function), html.EscapeString(g.Company))
			if rq.Phone {
				fmt.Fprintf(&b, `<td>%s</td>`, html.EscapeString(p.Phone))
			}
			if rq.Shift {
				fmt.Fprintf(&b, `<td>%s</td>`, map[string]string{"Day": "Jour", "Night": "Nuit"}[p.Shift])
			}
			b.WriteString(`</tr>`)
		}
		fmt.Fprintf(&b, `<tr class="tot"><td colspan="%d">TOTAL</td><td>%d</td>`, cols-1-map[bool]int{true: 1}[rq.Phone]-map[bool]int{true: 1}[rq.Shift], len(g.People))
		if rq.Phone {
			b.WriteString(`<td></td>`)
		}
		if rq.Shift {
			b.WriteString(`<td></td>`)
		}
		b.WriteString(`</tr></tbody></table>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func (a *app) pobHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hub/person/move", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs  []string `json:"ids"`
			Type string   `json:"type"` // out | in
			When string   `json:"when"`
			Note string   `json:"note"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "changed": a.hub.move(in.IDs, in.Type, in.When, in.Note)})
	}))
	mux.HandleFunc("/api/hub/pob/export", guard(func(w http.ResponseWriter, r *http.Request) {
		var rq pobReq
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&rq) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		people := a.pobPeople(rq)
		if len(people) == 0 {
			http.Error(w, "no people", 400)
			return
		}
		d := rq.Date
		if d == "" {
			d = time.Now().Format("2006-01-02")
		}
		a.sendFile(w, "POB_"+strings.ReplaceAll(d, "-", "")+".xlsx", buildPOBXlsx(groupByCompany(people), rq))
	}))
	// printable page (open in a new tab, then Ctrl+P / save as PDF)
	mux.HandleFunc("/api/hub/pob/print", guard(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rq := pobReq{Date: q.Get("date"), Rig: q.Get("rig"), Well: q.Get("well"), Phone: q.Get("phone") == "1", Shift: q.Get("shift") == "1", Photo: q.Get("photo") == "1"}
		if c := q.Get("companies"); c != "" {
			rq.Companies = strings.Split(c, "|")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pobPrintHTML(groupByCompany(a.pobPeople(rq)), rq)))
	}))
	// archive: everybody who was on board during a period, with the stays
	mux.HandleFunc("/api/hub/pob/archive", guard(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, to := q.Get("from"), q.Get("to")
		if from == "" {
			from = "0000-01-01"
		}
		if to == "" {
			to = "9999-12-31"
		}
		f, _ := dayRange(from)
		_, t := dayRange(to)
		s := lc(q.Get("q"))
		var out []Person
		a.hub.mu.Lock()
		for _, p := range a.hub.h.People {
			if !p.onBoardBetween(f, t) {
				continue
			}
			if s != "" && !strings.Contains(lc(p.Name+" "+p.Company+" "+p.Function), s) {
				continue
			}
			out = append(out, p)
		}
		a.hub.mu.Unlock()
		writeJSON(w, map[string]interface{}{"people": out})
	}))
}
