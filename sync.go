package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ptwPrefix = map[string]string{"LIFTING": "LWP", "COLD": "COLD", "HOT": "HOT", "CONFINED": "CSE", "ELECTRICAL": "ELEC", "HEIGHT": "WAH", "EXCAVATION": "EXC"}

func ptwKey(t string) string {
	u := strings.ToUpper(t)
	for k := range ptwPrefix {
		if strings.Contains(u, k) {
			return k
		}
	}
	if strings.Contains(u, "COLED") {
		return "COLD"
	}
	return ""
}

// serial "01-NN" (running number across the log) and default JSA reference by type
func (a *app) ptwSerial(r Rec) {
	if r.S("serial") == "" {
		pre := a.setting("ptwBatch")
		if pre == "" {
			pre = "01"
		}
		n := 1 // numbering restarts every month, like the monthly PTW log
		for _, o := range a.hub.recsOf("ptw") {
			if d := r.S("date"); len(d) >= 7 && len(o.S("date")) >= 7 && o.S("date")[:7] == d[:7] {
				n++
			}
		}
		r["serial"] = fmt.Sprintf("%s-%02d", pre, n)
	}
	if r.S("jsa") == "" {
		if k := ptwKey(r.S("type")); k != "" {
			y := r.S("date")
			if len(y) >= 4 {
				y = y[:4]
			}
			r["jsa"] = ptwPrefix[k] + y + "01"
		}
	}
}

func buildPTWXlsx(rig, well string, list []Rec) []byte {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].S("date") != list[j].S("date") {
			return list[i].S("date") < list[j].S("date")
		}
		return list[i].S("serial") < list[j].S("serial")
	})
	sh := XSheet{Name: "Sheet1", Fit: true, Widths: map[int]int{0: 10, 1: 18, 2: 12, 3: 38, 4: 14, 5: 14, 6: 22}}
	sh.Rows = append(sh.Rows, []XCell{xt("Permit To Work Log Sheet", 3)}, nil,
		[]XCell{xt("Rig Name: "+rig, 6), {}, {}, {}, xt("WELL NAME "+well, 6)},
		[]XCell{xt("Serial No.", 7), xt("Type Of PTW", 7), xt("Date", 7), xt("work descreption", 7), xt("Commence Time", 7), xt("Closed Time", 7), xt("JSA Ref No/Remark", 7)})
	sh.PrintTitles = "4:4"
	prev := ""
	for _, r := range list {
		dst := 4
		if r.S("date") != prev { // day separator: yellow date cell on the first permit of each day
			dst, prev = 10, r.S("date")
		}
		sh.Rows = append(sh.Rows, []XCell{xt(r.S("serial"), 4), xt(strings.ToUpper(r.S("type")), 4), xt(dmy(r.S("date")), dst), xt(r.S("desc"), 2), xt(hhmm(r.S("start")), 4), xt(hhmm(r.S("end")), 4), xt(r.S("jsa"), 4)})
	}
	return buildBook([]XSheet{sh})
}

func hhmm(s string) string { // 07:00 -> 07H00
	s = strings.TrimSpace(s)
	if len(s) == 5 && s[2] == ':' {
		return s[:2] + "H" + s[3:]
	}
	return s
}

func (a *app) rigWell() (string, string) {
	rig, well := a.setting("rig"), a.setting("well")
	if rig == "" {
		rig = "SP-236"
	}
	return rig, well
}

// afterSave keeps the Excel files in Documents in step with the data.
func (a *app) afterSave(kind string) {
	defer func() { _ = recover() }()
	a.syncPeopleFile()
	if kind == "ptw" {
		rig, well := a.rigWell()
		dir := filepath.Join(a.docsRoot(), "PTW")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "PTW_Log.xlsx"), buildPTWXlsx(rig, well, a.hub.recsOf("ptw")), 0o644)
	}
}

// People.xlsx = current POB (same format as the Sonatrach POB), rewritten after every change.
func (a *app) syncPeopleFile() {
	rig, well := a.rigWell()
	rq := pobReq{Rig: rig, Well: well, Phone: true, Shift: true}
	people := a.pobPeople(rq)
	if len(people) == 0 {
		return
	}
	rq.Date = time.Now().Format("2006-01-02")
	data := buildPOBXlsx(groupByCompany(people), rq)
	mp := a.peopleMasterPath()
	_ = os.MkdirAll(filepath.Dir(mp), 0o755)
	_ = os.WriteFile(mp, data, 0o644) // master file on the flash drive
	dir := filepath.Join(a.docsRoot(), "People")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "People_POB.xlsx"), data, 0o644)
}

// the people file that lives on the flash drive: edited here, never re-imported
func (a *app) peopleMasterPath() string {
	return filepath.Join(a.driveDir(), "People", "People_POB_Master.xlsx")
}

// syncLoop rewrites People_POB.xlsx whenever the data file changes (people added/moved/removed).
func (a *app) syncLoop() {
	var last time.Time
	for {
		a.dailyBackup()
		time.Sleep(15 * time.Second)
		if st, err := os.Stat(a.hub.path); err == nil && st.ModTime().After(last) {
			last = st.ModTime()
			a.syncPeopleFile()
		}
	}
}

// ptwMonthTo: permits of the month up to `date` (the monthly PTW log grows day by day).
func (a *app) ptwMonthTo(date string) []Rec {
	var out []Rec
	for _, r := range a.hub.recsOf("ptw") {
		if d := r.S("date"); len(d) >= 7 && len(date) >= 7 && d[:7] == date[:7] && d <= date {
			out = append(out, r)
		}
	}
	return out
}

// dailyBackup keeps one copy of the data file per day on the drive (last 14 days).
func (a *app) dailyBackup() {
	defer func() { _ = recover() }()
	dir := filepath.Join(filepath.Dir(a.hub.path), "backup")
	dst := filepath.Join(dir, "hse-"+time.Now().Format("20060102")+".json")
	if _, err := os.Stat(dst); err == nil {
		return
	}
	copyFile(a.hub.path, dst)
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "hse-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > 14 {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}
