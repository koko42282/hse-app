package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// "LOCAL FIELD PAYROLL TIME SHEET" layout: one block per company and crew (header, table N/Full Name/Position/Category/days/Total, legend).

var monthAbbr = []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}

type rotSpec struct {
	Start string `json:"start"` // "W" | "H" | "" (blank)
	Off   int    `json:"off"`   // days already spent in the starting state before day 1
}

func rotCode(spec rotSpec, wd, hd, day0 int) string {
	if spec.Start != "W" && spec.Start != "H" {
		return ""
	}
	cyc := wd + hd
	if cyc <= 0 {
		return ""
	}
	pos := (day0 + spec.Off) % cyc
	first, fl, second := "W", wd, "H"
	if spec.Start == "H" {
		first, fl, second = "H", hd, "W"
	}
	if pos < fl {
		return first
	}
	return second
}

func crewSortKey(c string) string {
	if c == "" {
		return "~"
	}
	if strings.EqualFold(c, "day") {
		return "!"
	}
	return strings.ToLower(c)
}

func buildTimesheet(people []Person, rq pointageReq) []byte {
	t, err := time.Parse("2006-01", rq.Month)
	if err != nil {
		t = time.Now()
	}
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	days := first.AddDate(0, 1, -1).Day()
	wd, hd := rq.Wdays, rq.Hdays
	if wd <= 0 {
		wd = 14
	}
	if hd <= 0 {
		hd = 14
	}
	type key struct{ co, crew string }
	groups := map[key][]Person{}
	var keys []key
	for _, p := range people {
		k := key{p.Company, p.Crew}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], p)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if !strings.EqualFold(keys[i].co, keys[j].co) {
			return strings.ToLower(keys[i].co) < strings.ToLower(keys[j].co)
		}
		return crewSortKey(keys[i].crew) < crewSortKey(keys[j].crew)
	})
	title := rq.Title
	if title == "" {
		title = "LOCAL FIELD PAYROLL TIME SHEET"
	}
	sh := XSheet{Name: monthAbbr[first.Month()-1] + "-" + fmt.Sprint(first.Year()), Widths: map[int]int{0: 4, 1: 30, 2: 20, 3: 10, 4 + days: 7}}
	for d := 0; d < days; d++ {
		sh.Widths[4+d] = 3
	}
	empty := func() { sh.Rows = append(sh.Rows, nil) }
	put := func(cells map[int]XCell) {
		max := 0
		for c := range cells {
			if c > max {
				max = c
			}
		}
		row := make([]XCell, max+1)
		for c, v := range cells {
			row[c] = v
		}
		sh.Rows = append(sh.Rows, row)
	}
	last := 4 + days // total column index
	for gi, k := range keys {
		if gi > 0 {
			empty()
			empty()
		}
		put(map[int]XCell{1: xt(title, 3)})
		empty()
		crewTxt := ""
		if k.crew != "" {
			crewTxt = "CREW " + k.crew
		}
		hdr := map[int]XCell{1: xt(rq.Site, 6), 2: xt(crewTxt, 6), 7: xt("CONTRACTOR", 6), 13: xt(k.co, 6)}
		if last >= 24 {
			hdr[21] = xt("MONTH / YEAR", 6)
			hdr[29] = xt(monthAbbr[first.Month()-1], 6)
			hdr[31] = xt(fmt.Sprint(first.Year()), 6)
		}
		put(hdr)
		empty()
		th := map[int]XCell{0: xt("N", 7), 1: xt("Full Name", 7), 2: xt("Position", 7), 3: xt("Category", 7)}
		for d := 1; d <= days; d++ {
			th[3+d] = xt(fmt.Sprint(d), 7)
		}
		th[last] = xt("Total", 7)
		put(th)
		r0 := len(sh.Rows) + 1
		for i, p := range groups[k] {
			row := map[int]XCell{0: xt(fmt.Sprint(i+1), 4), 1: xt(p.Name, 2), 2: xt(p.Function, 2), 3: xt(p.Category, 4)}
			spec := rq.Rot[k.crew]
			for d := 0; d < days; d++ {
				code := rotCode(spec, wd, hd, d)
				if df, dt := dayRange(first.AddDate(0, 0, d).Format("2006-01-02")); !p.onBoardBetween(df, dt) {
					code = "/"
				}
				if code == "" {
					row[4+d] = xt("", 4)
				} else {
					row[4+d] = xt(code, 4)
				}
			}
			row[last] = xf(fmt.Sprintf("COUNTIF(%s%d:%s%d,\"W\")", colName(4), r0+i, colName(3+days), r0+i), 4)
			put(row)
		}
		empty()
		put(map[int]XCell{1: xt("LEGEND", 6)})
		for _, l := range []string{"W : DAYS WORKED", "H : DAYS OFF", "A : ABSENT", "S : SICK"} {
			put(map[int]XCell{1: xt(l, 0)})
		}
	}
	return buildBook([]XSheet{sh})
}

var timesheetCrewRe = regexp.MustCompile(`(?i)^\s*crew\b`)

// importTimesheetPeople reads the people (name, position, category, crew, company) of a payroll time sheet workbook.
func importTimesheetPeople(path string, sheetIdx int) ([]Person, string, error) {
	grids, err := readGrids(path)
	if err != nil {
		return nil, "", err
	}
	if len(grids) == 0 {
		return nil, "", fmt.Errorf("الملف فارغ")
	}
	// the last sheet that has a time-sheet table (most recent month)
	var pick *sheetGrid
	for i := len(grids) - 1; i >= 0; i-- {
		for _, row := range grids[i].Rows {
			for _, c := range row {
				if strings.EqualFold(strings.TrimSpace(c), "full name") {
					pick = &grids[i]
				}
			}
		}
		if pick != nil {
			break
		}
	}
	if pick == nil {
		return nil, "", fmt.Errorf("لم أجد جدول Full Name في الملف")
	}
	var out []Person
	g := pick.Rows
	for r := 0; r < len(g); r++ {
		nc := -1
		for c, v := range g[r] {
			if strings.EqualFold(strings.TrimSpace(v), "full name") {
				nc = c
				break
			}
		}
		if nc < 0 {
			continue
		}
		crew, company := "", ""
		for rr := r - 1; rr >= 0 && rr >= r-5; rr-- {
			for c, v := range g[rr] {
				v = strings.TrimSpace(v)
				if crew == "" && timesheetCrewRe.MatchString(v) && !strings.ContainsAny(v, ":") {
					crew = normCrew(v)
				}
				if strings.EqualFold(v, "contractor") && company == "" {
					for k := c + 1; k < len(g[rr]); k++ {
						if x := strings.TrimSpace(g[rr][k]); x != "" {
							company = x
							break
						}
					}
				}
			}
		}
		if crew == "" {
			crew = "Day"
		}
		for rr := r + 1; rr < len(g); rr++ {
			name := gridCell(g, rr, nc)
			if name == "" || strings.EqualFold(name, "legend") {
				break
			}
			p := Person{Name: normSpaces(name), Function: normSpaces(gridCell(g, rr, nc+1)), Category: normSpaces(gridCell(g, rr, nc+2)), Company: normSpaces(company), Crew: crew}
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("لا أسماء في الجدول")
	}
	return out, pick.Name, nil
}

// addPeople merges people (by name+company); existing ones only get missing fields (crew, category...).
func (s *hubStore) addPeople(list []Person) (added, updated int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := map[string]int{}
	for i, p := range s.h.People {
		idx[strings.ToLower(p.Name)+"|"+strings.ToLower(p.Company)] = i
	}
	for _, p := range list {
		k := strings.ToLower(p.Name) + "|" + strings.ToLower(p.Company)
		if i, ok := idx[k]; ok {
			e := &s.h.People[i]
			ch := false
			if e.Crew == "" && p.Crew != "" {
				e.Crew, ch = p.Crew, true
			}
			if e.Function == "" && p.Function != "" {
				e.Function, ch = p.Function, true
			}
			if e.Category == "" && p.Category != "" {
				e.Category, ch = p.Category, true
			}
			if ch {
				updated++
			}
			continue
		}
		p.ID = newID()
		p.Added = time.Now().Format("2006-01-02")
		p.Stays = []Stay{{In: time.Now().Format("2006-01-02T15:04")}}
		s.h.People = append(s.h.People, p)
		idx[k] = len(s.h.People) - 1
		added++
	}
	_ = s.saveLocked()
	return
}

func (a *app) timesheetImportHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(r.URL.Query().Get("name"))
	ext := ".xlsx"
	if strings.HasSuffix(name, ".xls") {
		ext = ".xls"
	}
	tmp, err := os.CreateTemp("", "ts*"+ext)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, http.MaxBytesReader(w, r.Body, 40<<20))
	tmp.Close()
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	people, sheet, err := importTimesheetPeople(tmp.Name(), -1)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	if c := strings.TrimSpace(r.URL.Query().Get("company")); c != "" {
		for i := range people {
			if people[i].Company == "" {
				people[i].Company = c
			}
		}
	}
	add, upd := a.hub.addPeople(people)
	writeJSON(w, map[string]interface{}{"ok": true, "sheet": sheet, "read": len(people), "added": add, "updated": upd})
}
