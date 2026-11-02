package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type pointageReq struct {
	Companies []string           `json:"companies"`
	Mode      string             `json:"mode"`  // "month" | "day"
	Month     string             `json:"month"` // 2026-10
	Date      string             `json:"date"`  // 2026-10-09
	Site      string             `json:"site"`
	Title     string             `json:"title"`
	Crews     []string           `json:"crews"`
	Wdays     int                `json:"wdays"`
	Hdays     int                `json:"hdays"`
	Rot       map[string]rotSpec `json:"rot"`
}

// peopleOf returns the people of the given companies (all when empty), sorted by company then name.
func (h *hubStore) peopleOf(companies []string, from, to string) []Person {
	h.mu.Lock()
	defer h.mu.Unlock()
	want := map[string]bool{}
	for _, c := range companies {
		want[strings.ToLower(strings.TrimSpace(c))] = true
	}
	var out []Person
	for _, p := range h.h.People {
		if !p.onBoardBetween(from, to) {
			continue
		}
		if len(want) == 0 || want[strings.ToLower(p.Company)] {
			out = append(out, p)
		}
	}
	// companies alphabetical; inside a company keep the order of the POB list (positions in the rig order)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Company) < strings.ToLower(out[j].Company)
	})
	return out
}

func buildPointage(people []Person, rq pointageReq) ([]byte, string) {
	by := map[string][]Person{}
	var order []string
	for _, p := range people {
		c := p.Company
		if c == "" {
			c = "-"
		}
		if _, ok := by[c]; !ok {
			order = append(order, c)
		}
		by[c] = append(by[c], p)
	}
	title := rq.Title
	if title == "" {
		title = "POINTAGE"
	}
	var sheets []XSheet
	sum := XSheet{Name: "Summary", Widths: map[int]int{0: 26, 1: 12}}
	sum.Rows = append(sum.Rows, []XCell{xt(title, 3)})
	period := rq.Date
	days := 0
	var first time.Time
	if rq.Mode == "month" {
		t, err := time.Parse("2006-01", rq.Month)
		if err != nil {
			t = time.Now()
		}
		first = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		days = first.AddDate(0, 1, -1).Day()
		period = first.Format("01/2006")
	} else if period == "" {
		period = time.Now().Format("2006-01-02")
	}
	sum.Rows = append(sum.Rows, []XCell{xt(strings.TrimSpace(rq.Site+"  "+period), 0)}, nil)
	sum.Rows = append(sum.Rows, []XCell{xt("Company", 1), xt("POB", 1), xt(map[bool]string{true: "Presence-days", false: "Present"}[rq.Mode == "month"], 1)})
	sumFirst := len(sum.Rows) + 1
	for i, c := range order {
		sn := sheetName(c, i)
		sh := XSheet{Name: sn, Freeze: "E5"}
		sh.Widths = map[int]int{0: 5, 1: 28, 2: 22, 3: 16}
		sh.Rows = append(sh.Rows, []XCell{xt(title+" — "+c, 3)})
		sh.Rows = append(sh.Rows, []XCell{xt(strings.TrimSpace(rq.Site+"  "+period), 0)}, nil)
		var hdr []XCell
		hdr = append(hdr, xt("N°", 7), xt("Name", 7), xt("Function", 7), xt("Category", 7))
		if rq.Mode == "month" {
			for d := 1; d <= days; d++ {
				hdr = append(hdr, xt(fmt.Sprint(d), 7))
				sh.Widths[3+d] = 3
			}
			hdr = append(hdr, xt("Total", 7))
			sh.Widths[4+days] = 7
		} else {
			hdr = append(hdr, xt("Present", 7), xt("Absent", 7), xt("Signature", 7), xt("Remarks", 7))
			sh.Widths[4], sh.Widths[5], sh.Widths[6], sh.Widths[7] = 9, 9, 18, 24
		}
		sh.Rows = append(sh.Rows, hdr)
		r0 := len(sh.Rows) + 1
		for k, p := range by[c] {
			row := []XCell{xt(fmt.Sprint(k+1), 4), xt(p.Name, 2), xt(p.Function, 2), xt(p.Category, 2)}
			if rq.Mode == "month" {
				for d := 1; d <= days; d++ {
					st := 4
					if wd := first.AddDate(0, 0, d-1).Weekday(); wd == time.Friday || wd == time.Saturday {
						st = 5
					}
					row = append(row, xt("", st))
				}
				row = append(row, xf(fmt.Sprintf("COUNTIF(%s%d:%s%d,\"P\")", colName(4), r0+k, colName(3+days), r0+k), 4))
			} else {
				row = append(row, xt("", 4), xt("", 4), xt("", 2), xt("", 2))
			}
			sh.Rows = append(sh.Rows, row)
		}
		r1 := r0 + len(by[c]) - 1
		if rq.Mode == "month" {
			t2 := make([]XCell, 4+days+1)
			t2[1] = xt("TOTAL", 6)
			t2[4+days] = xf(fmt.Sprintf("SUM(%s%d:%s%d)", colName(4+days), r0, colName(4+days), r1), 6)
			for d := 1; d <= days; d++ {
				t2[3+d] = xf(fmt.Sprintf("COUNTIF(%s%d:%s%d,\"P\")", colName(3+d), r0, colName(3+d), r1), 4)
			}
			sh.Rows = append(sh.Rows, t2)
		} else {
			t2 := make([]XCell, 8)
			t2[1] = xt("TOTAL", 6)
			t2[4] = xf(fmt.Sprintf("COUNTIF(E%d:E%d,\"P\")", r0, r1), 6)
			t2[5] = xf(fmt.Sprintf("COUNTIF(F%d:F%d,\"A\")", r0, r1), 6)
			sh.Rows = append(sh.Rows, t2)
		}
		sheets = append(sheets, sh)
		q := "'" + strings.ReplaceAll(sn, "'", "''") + "'"
		pres := fmt.Sprintf("COUNTA(%s!B%d:B%d)", q, r0, r1)
		var third XCell
		if rq.Mode == "month" {
			third = xf(fmt.Sprintf("%s!%s%d", q, colName(4+days), r1+1), 2)
		} else {
			third = xf(fmt.Sprintf("%s!E%d", q, r1+1), 2)
		}
		sum.Rows = append(sum.Rows, []XCell{xt(c, 2), xf(pres, 4), third})
	}
	n := len(order)
	sum.Rows = append(sum.Rows, []XCell{xt("TOTAL", 6), xf(fmt.Sprintf("SUM(B%d:B%d)", sumFirst, sumFirst+n-1), 6), xf(fmt.Sprintf("SUM(C%d:C%d)", sumFirst, sumFirst+n-1), 6)})
	if n > 0 {
		sum.Charts = append(sum.Charts, XChart{Type: "col", Title: "POB per company", Cats: fmt.Sprintf("A%d:A%d", sumFirst, sumFirst+n-1), Series: []XSeries{{Name: "POB", Ref: fmt.Sprintf("B%d:B%d", sumFirst, sumFirst+n-1)}}, From: [2]int{4, 2}, To: [2]int{11, 18}})
	}
	sheets = append([]XSheet{sum}, sheets...)
	return buildBook(sheets), "pointage"
}

func sheetName(c string, i int) string {
	r := []rune(c)
	if len(r) > 24 {
		r = r[:24]
	}
	s := strings.Map(func(x rune) rune {
		if strings.ContainsRune(`[]:*?/\'`, x) {
			return '_'
		}
		return x
	}, string(r))
	if s == "" || strings.EqualFold(s, "Summary") {
		s = fmt.Sprintf("Co%d", i+1)
	}
	return s
}

func (a *app) handlePointage(w http.ResponseWriter, r *http.Request) {
	var rq pointageReq
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&rq) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	from, to := dayRange(firstOf(rq.Date, time.Now().Format("2006-01-02")))
	if rq.Mode != "day" {
		t, err := time.Parse("2006-01", rq.Month)
		if err != nil {
			t = time.Now()
		}
		f := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		from, _ = dayRange(f.Format("2006-01-02"))
		_, to = dayRange(f.AddDate(0, 1, -1).Format("2006-01-02"))
	}
	people := a.hub.peopleOf(rq.Companies, from, to)
	if len(people) == 0 {
		http.Error(w, "no people", 400)
		return
	}
	if len(rq.Crews) > 0 {
		want := map[string]bool{}
		for _, c := range rq.Crews {
			want[c] = true
		}
		var f []Person
		for _, p := range people {
			if want[p.Crew] {
				f = append(f, p)
			}
		}
		if people = f; len(people) == 0 {
			http.Error(w, "no people", 400)
			return
		}
	}
	var data []byte
	if rq.Mode == "timesheet" {
		data = buildTimesheet(people, rq)
	} else {
		data, _ = buildPointage(people, rq)
	}
	name := "Pointage_" + strings.NewReplacer("-", "", "/", "").Replace(firstOf(rq.Month, rq.Date, time.Now().Format("2006-01-02"))) + ".xlsx"
	a.sendFile(w, name, data)
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
