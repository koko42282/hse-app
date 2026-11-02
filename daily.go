package main

// Sonatrach daily HSE report computed on the server from the recorded data.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var xlEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

func xlSerial(d string) int {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return 0
	}
	return int(t.Sub(xlEpoch).Hours() / 24)
}
func fromSerial(s string) (time.Time, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 30000 || f > 80000 {
		return time.Time{}, false
	}
	return xlEpoch.AddDate(0, 0, int(f)), true
}

type pv struct {
	Cell  string `json:"cell"`
	Label string `json:"label"`
	Value string `json:"value"`
	Group string `json:"group"`
	Auto  bool   `json:"auto"`
}

type dailyIn struct {
	Date   string            `json:"date"`
	BaseID string            `json:"baseId"`
	Ov     map[string]string `json:"ov"` // manual overrides cell -> value
}

func fnum(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(strings.Replace(s, ",", ".", 1)), 64)
	return f
}
func itoa(n int) string { return strconv.Itoa(n) }
func ftoa(f float64) string {
	return strconv.FormatFloat(math.Round(f*100)/100, 'f', -1, 64)
}

var incidentRows = map[string]int{"LTI": 11, "LOSTDAYS": 12, "MTC": 13, "FAC": 14, "INC": 15, "NM": 16, "ENV": 17, "OTHER": 18, "FALL": 19}
var incidentLabel = map[string]string{"LTI": "Lost Time Injury", "LOSTDAYS": "No. Lost Days", "MTC": "Med Treat Only", "FAC": "First Aid Case", "INC": "Incident", "NM": "Near-Miss", "ENV": "Env. Spill", "OTHER": "Others", "FALL": "Fall from height"}
var inspRows = map[string]int{"camp": 11, "rig": 12, "mast": 13, "fire": 14, "lifting": 15, "crane": 16, "env": 17, "other": 18}
var inspLabel = map[string]string{"camp": "Camp, Hygiene", "rig": "Rig General", "mast": "Mast", "fire": "Fire Equipt", "lifting": "Lifting Equipt", "crane": "Crane / Forklift", "env": "Environment", "other": "Others"}
var drillRow = map[string]int{"kick": 11, "kick_drilling": 11, "fire": 13, "medevac": 15, "security": 17, "environment": 17, "other": 17}
var drillTitle = map[string]string{"kick": "Kick Drill", "kick_drilling": "Kick Drill (Drilling)", "fire": "Fire Drill", "medevac": "Medevac Drill", "security": "Security Drill", "environment": "Environment Drill", "other": "Drill"}

// default company -> report row suggestions (row 10..17)
func suggestRow(company string) int {
	c := strings.ToLower(company)
	switch {
	case strings.Contains(c, "sonatrach"), strings.Contains(c, "dp"), c == "df":
		return 10
	case strings.Contains(c, "integrat"), strings.Contains(c, "sinopec"), strings.Contains(c, "cnpc"), strings.Contains(c, "chin"):
		return 11
	case strings.Contains(c, "osco"), strings.Contains(c, "drilling"), strings.Contains(c, "contract"):
		return 12
	case strings.Contains(c, "cater"), strings.Contains(c, "cook"), strings.Contains(c, "restaur"), strings.Contains(c, "ligne"):
		return 13
	case strings.Contains(c, "secur"), strings.Contains(c, "garde"), strings.Contains(c, "civil"):
		return 14
	case strings.Contains(c, "milit"), strings.Contains(c, "army"), strings.Contains(c, "anp"):
		return 15
	}
	return 16
}

// stopsMonthTo: every card of the month up to and including `date`, oldest first (the monthly Sonatrach log grows day by day).
func (a *app) stopsMonthTo(date string) []StopCard {
	if len(date) < 7 {
		return nil
	}
	a.hub.mu.Lock()
	var out []StopCard
	for _, c := range a.hub.h.Stops {
		if len(c.Date) >= 7 && c.Date[:7] == date[:7] && c.Date <= date {
			out = append(out, c)
		}
	}
	a.hub.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func (a *app) stopsOn(date string) []StopCard {
	a.hub.mu.Lock()
	defer a.hub.mu.Unlock()
	var out []StopCard
	for _, c := range a.hub.h.Stops {
		if c.Date == date {
			out = append(out, c)
		}
	}
	return out
}

func (a *app) computeDaily(in dailyIn) ([]pv, map[string]string, []string) {
	date := in.Date
	h := a.hub
	if in.BaseID == "" {
		in.BaseID, _ = a.bestBase(date)
	}
	base, _, _ := a.readBase(in.BaseID)
	if base == nil {
		base = map[string]string{}
	}
	var prev time.Time
	same := false
	if t, ok := fromSerial(base["N2"]); ok {
		prev = t
		same = t.Format("2006-01")+"" == date[:7] && t.Format("2006-01-02") != date
		if t.Format("2006-01-02") == date {
			same = true // re-generating the same day: monthly values already include it
		}
	}
	rebuild := !prev.IsZero() && prev.Format("2006-01-02") == date
	var warns []string
	var out []pv
	cells := map[string]string{}
	set := func(cell, group, label, val string) {
		cells[cell] = val
		out = append(out, pv{cell, label, val, group, true})
	}
	// monthly baseline: previous monthly value (0 when the month changed). When regenerating the same day we cannot know
	// the previous day's cumul, so warn.
	cb := func(cell string) float64 {
		if !same {
			return 0
		}
		return fnum(base[cell])
	}
	if rebuild {
		warns = append(warns, "القاعدة المختارة هي تقرير نفس اليوم؛ اختر تقرير اليوم السابق كأساس حتى لا تتضاعف المجاميع الشهرية.")
	}
	if prev.IsZero() {
		warns = append(warns, "لم يتم اختيار تقرير سابق كأساس: المجاميع الشهرية تبدأ من الصفر.")
	}
	g := "الرأس"
	set("N2", g, "Date", itoa(xlSerial(date)))
	rep := int(fnum(base["C3"])) + 1
	if rebuild {
		rep = int(fnum(base["C3"])) // regenerating the base's own day keeps its number
	} else if !prev.IsZero() && !same {
		rep = 1 // new month: numbering restarts at 1
	}
	set("C3", g, "Rep No", itoa(rep))
	rig, well := a.rigWell()
	if a.setting("rig") == "" && base["E3"] != "" {
		rig = base["E3"]
	}
	if well == "" {
		well = base["H3"]
	}
	field := a.setting("field")
	if field == "" {
		field = base["K3"]
	}
	sup := a.setting("hseName")
	if sup == "" {
		sup = base["N3"]
	}
	set("E3", g, "Rig #", rig)
	set("H3", g, "Well #", well)
	set("K3", g, "Field", field)
	set("N3", g, "Rig HSE Sup.", sup)

	// LTI
	lti := a.setting("lastLTI")
	if lti == "" {
		if m := regexp.MustCompile(`(\d{2})/(\d{2})/(\d{4})`).FindStringSubmatch(base["A5"]); m != nil {
			lti = m[3] + "-" + m[2] + "-" + m[1]
		}
	}
	incs := h.recsOn("incident", date)
	for _, r := range incs {
		if r.S("kind") == "LTI" && (lti == "" || date >= lti) {
			lti = date
		}
	}
	if lti != "" {
		t, _ := time.Parse("2006-01-02", lti)
		d, _ := time.Parse("2006-01-02", date)
		set("A5", g, "Date Last LTI", "Date Last LTI: "+strings.ReplaceAll(dmy(lti), "-", "/"))
		set("H5", g, "LTI Free Days", itoa(int(d.Sub(t).Hours()/24)))
	}
	if b := a.setting("bopTest"); b != "" {
		set("K5", g, "BOP Press Test", itoa(xlSerial(b)))
	}

	// inductions
	ind := 0
	for _, r := range h.recsOn("induction", date) {
		n := int(r.N("count"))
		if n == 0 {
			n = len(r.L("people"))
		}
		ind += n
	}
	set("O5", g, "No. Inductions (اليوم)", itoa(ind))
	set("M55", "Training", "Tot. Inductions/Month", ftoa(cb("M55")+float64(ind)))

	// operations
	ops := h.recsOn("ops", date)
	var otxt []string
	for _, o := range ops {
		if t := o.S("text"); t != "" {
			otxt = append(otxt, t)
		}
	}
	set("B22", "Current Drilling Ops", "Current Drilling Ops.", strings.Join(otxt, " / "))

	// accidents / incidents
	acc := map[string]int{}
	var brief []string
	for _, r := range incs {
		k := r.S("kind")
		if _, ok := incidentRows[k]; !ok {
			k = "INC"
		}
		acc[k]++
		if k == "LTI" {
			acc["LOSTDAYS"] += int(r.N("lostDays"))
		}
		if t := r.S("brief"); t != "" {
			brief = append(brief, t)
		} else if t := r.S("fr_event"); t != "" {
			brief = append(brief, t)
		}
	}
	for k, row := range incidentRows {
		set("D"+itoa(row), "Accident - Incident", incidentLabel[k]+" (اليوم)", itoa(acc[k]))
		set("E"+itoa(row), "Accident - Incident", incidentLabel[k]+" (الشهر)", ftoa(cb("E"+itoa(row))+float64(acc[k])))
	}
	if len(brief) > 0 {
		set("B7", "Accident - Incident", "Brief Accident Description", strings.Join(brief, " | "))
	}

	// inspections
	ins := map[string]int{}
	tot := 0
	for _, r := range h.recsOn("inspection", date) {
		k := r.S("area")
		if k == "extinguishers" {
			k = "fire" // extinguisher inspections count under "Fire Equipt"
		}
		if _, ok := inspRows[k]; !ok {
			k = "other"
		}
		n := 1
		if r.N("count") > 0 {
			n = int(r.N("count"))
		}
		ins[k] += n
		tot += n
	}
	for k, row := range inspRows {
		set("H"+itoa(row), "Inspection", inspLabel[k]+" (اليوم)", itoa(ins[k]))
		set("I"+itoa(row), "Inspection", inspLabel[k]+" (الشهر)", ftoa(cb("I"+itoa(row))+float64(ins[k])))
	}
	set("H19", "Inspection", "Inspection total (اليوم)", itoa(tot))
	set("I19", "Inspection", "Inspection total (الشهر)", ftoa(cb("I19")+float64(tot)))

	// drills (monthly counters = previous + today) and the matching training lines
	type tline struct {
		topic string
		n     int
		mins  int
	}
	var lines []tline
	dc := map[int]int{}
	dtot := 0
	for _, r := range h.recsOn("drill", date) {
		row := drillRow[r.S("type")]
		if row == 0 {
			row = 17
		}
		dc[row]++
		dtot++
		n := int(r.N("count"))
		if n == 0 {
			n = len(r.L("attendees"))
		}
		m := int(r.N("minutes"))
		lines = append(lines, tline{drillTitle[r.S("type")], n, m})
	}
	for _, row := range []int{11, 13, 15, 17} {
		set("J"+itoa(row), "Drills", "Drill "+map[int]string{11: "Kick", 13: "Fire", 15: "Medevac", 17: "Others/Security/Env"}[row]+" (الشهر)", ftoa(cb("J"+itoa(row))+float64(dc[row])))
	}
	set("J19", "Drills", "Drills total (الشهر)", ftoa(cb("J19")+float64(dtot)))
	for _, r := range h.recsOn("induction", date) {
		n := int(r.N("count"))
		if n == 0 {
			n = len(r.L("people"))
		}
		lines = append(lines, tline{"Induction (HSE site induction)", n, int(r.N("minutes"))})
	}
	for _, r := range h.recsOn("training", date) {
		n := int(r.N("count"))
		if n == 0 {
			n = len(r.L("attendees"))
		}
		t := r.S("title")
		lines = append(lines, tline{t, n, int(r.N("minutes"))})
	}

	// personnel
	var rpmap map[string][]string
	_ = json.Unmarshal([]byte(a.setting("rpmap")), &rpmap)
	company2row := map[string]int{}
	for r, cs := range rpmap {
		ri, _ := strconv.Atoi(r)
		for _, c := range cs {
			company2row[strings.ToLower(c)] = ri
		}
	}
	rows := map[int]int{}
	for _, p := range a.pobPeople(pobReq{Date: date}) {
		r, ok := company2row[strings.ToLower(p.Company)]
		if !ok {
			r = suggestRow(p.Company)
		}
		rows[r]++
	}
	hours := fnum(a.setting("hours"))
	if hours == 0 {
		hours = 12
	}
	sum, mil := 0, 0
	for r := 10; r <= 17; r++ {
		n := rows[r]
		set("N"+itoa(r), "Personnel", base["K"+itoa(r)]+" (اليوم)", itoa(n))
		set("O"+itoa(r), "Personnel", base["K"+itoa(r)]+" (الشهر)", ftoa(cb("O"+itoa(r))+float64(n)))
		sum += n
		if strings.Contains(strings.ToLower(base["K"+itoa(r)]), "militar") {
			mil += n
		}
	}
	nm := sum - mil
	set("O18", "Personnel", "Total Personnel (-Military) الشهر", ftoa(cb("O18")+float64(nm)))
	set("N19", "Personnel", "Total MHrs (-Military) اليوم", ftoa(float64(nm)*hours))
	set("O19", "Personnel", "Total MHrs (-Military) الشهر", ftoa(cb("O19")+float64(nm)*hours))
	if sum == 0 {
		warns = append(warns, "لا يوجد أشخاص على POB في هذا التاريخ.")
	}

	// STOP
	cards := a.stopsOn(date)
	set("E24", "STOP", "STOP Cards (اليوم)", itoa(len(cards)))
	set("M24", "STOP", "STOP Cards (الشهر)", ftoa(cb("M24")+float64(len(cards))))
	for i := 0; i < 10; i++ {
		n, d, s := "", "", ""
		if i < len(cards) {
			n, d = itoa(i+1), cards[i].Desc
			s = "CLOSE"
			if cards[i].Status == "Open" {
				s = "OPEN"
			}
		}
		set("A"+itoa(25+i), "STOP", "STOP "+itoa(i+1), n)
		set("B"+itoa(25+i), "STOP", "STOP "+itoa(i+1)+" — الوصف", d)
		set("O"+itoa(25+i), "STOP", "STOP "+itoa(i+1)+" — الحالة", s)
	}
	if len(cards) > 10 {
		warns = append(warns, fmt.Sprintf("%d بطاقة STOP اليوم؛ تُكتب أول 10 فقط في التقرير (العدد الكامل في E24).", len(cards)))
	}

	// PTW + JSA
	ptws := h.recsOn("ptw", date)
	var pl, jl []string
	for _, p := range ptws {
		pl = append(pl, strings.TrimSpace(p.S("type")+" — "+p.S("desc")))
		if j := p.S("jsa"); j != "" {
			jl = append(jl, "JSA "+j+": "+p.S("desc"))
		}
	}
	for _, o := range ops {
		j := o.S("jsa")
		if j == "" {
			j = "JSA"
		}
		jl = append(jl, j+" — "+o.S("text"))
	}
	set("E35", "PTW", "PTW (اليوم)", itoa(len(ptws)))
	set("O35", "PTW", "PTW (الشهر)", ftoa(cb("O35")+float64(len(ptws))))
	set("E43", "JSA", "JSA (اليوم)", itoa(len(jl)))
	set("O43", "JSA", "JSA (الشهر)", ftoa(cb("O43")+float64(len(jl))))
	for k := 0; k < 7; k++ {
		n1, t1, n2, t2 := "", "", "", ""
		if k < len(pl) {
			n1, t1 = itoa(k+1), pl[k]
		}
		if k < len(jl) {
			n2, t2 = itoa(k+1), jl[k]
		}
		set("A"+itoa(36+k), "PTW", "PTW "+itoa(k+1), n1)
		set("B"+itoa(36+k), "PTW", "PTW "+itoa(k+1)+" — النص", t1)
		set("A"+itoa(44+k), "JSA", "JSA "+itoa(k+1), n2)
		set("B"+itoa(44+k), "JSA", "JSA "+itoa(k+1)+" — النص", t2)
	}
	if len(pl) > 7 || len(jl) > 7 {
		warns = append(warns, "أكثر من 7 تصاريح/JSA: تُكتب أول 7 فقط في التقرير (العدد الكامل في الخلايا E35/E43).")
	}

	// training lines (3 rows) + man-hours
	mh := 0.0
	for _, l := range lines {
		mh += float64(l.n*l.mins) / 60
	}
	set("F51", "Training", "Today MHrs", ftoa(mh))
	set("M51", "Training", "MHrs Trained /Month", ftoa(cb("M51")+mh))
	for i := 0; i < 3; i++ {
		var t string
		var n, m int
		if i < len(lines) {
			t, n, m = lines[i].topic, lines[i].n, lines[i].mins
			if i == 2 && len(lines) > 3 {
				for _, x := range lines[3:] {
					t += " + " + x.topic
					n = maxInt(n, x.n)
					m += x.mins
				}
				warns = append(warns, "أكثر من 3 مواضيع تدريب: دُمجت الزائدة في السطر الثالث (ساعات الرجال محسوبة كاملة).")
			}
		}
		if t == "" {
			set("A"+itoa(52+i), "Training", "Training "+itoa(i+1), "")
			set("B"+itoa(52+i), "Training", "Training "+itoa(i+1)+" — الموضوع", "")
			set("N"+itoa(52+i), "Training", "Training "+itoa(i+1)+" — أشخاص", "")
			set("O"+itoa(52+i), "Training", "Training "+itoa(i+1)+" — دقائق", "")
			continue
		}
		set("A"+itoa(52+i), "Training", "Training "+itoa(i+1), itoa(i+1))
		set("B"+itoa(52+i), "Training", "Training "+itoa(i+1)+" — الموضوع", t)
		set("N"+itoa(52+i), "Training", "Training "+itoa(i+1)+" — أشخاص", itoa(n))
		set("O"+itoa(52+i), "Training", "Training "+itoa(i+1)+" — دقائق", itoa(m))
	}

	// safety meetings
	mt := map[string][]Rec{}
	for _, r := range h.recsOn("meeting", date) {
		mt[r.S("mkind")] = append(mt[r.S("mkind")], r)
	}
	topics := func(rs []Rec) string {
		var t []string
		for _, r := range rs {
			if x := r.S("topic"); x != "" {
				t = append(t, x)
			}
		}
		return strings.Join(t, " / ")
	}
	cnt := func(k string, def int) int {
		if rs, ok := mt[k]; ok {
			return len(rs)
		}
		return def
	}
	pre := cnt("prejob", 0)
	var optop []string
	for _, o := range ops {
		if o.S("meetingTopic") != "" {
			optop = append(optop, o.S("meetingTopic"))
		}
	}
	if len(ops) > pre {
		pre = len(ops)
	}
	ms := []struct {
		row, trow int
		n         int
		topic     string
		label     string
	}{
		{56, 57, cnt("morning", 1), topics(mt["morning"]), "Morning Safety Meeting"},
		{58, 59, cnt("crew", 1), topics(mt["crew"]), "Crew change Pre-Tour"},
		{61, 62, pre, strings.Join(append(optop, topics(mt["prejob"])), " / "), "Pre-Job / TBT"},
		{65, 0, cnt("weekly", 0), topics(mt["weekly"]), "Weekly Safety Meeting"},
	}
	for _, m := range ms {
		set("N"+itoa(m.row), "Safety Meetings", m.label+" (اليوم)", itoa(m.n))
		set("O"+itoa(m.row), "Safety Meetings", m.label+" (الشهر)", ftoa(cb("O"+itoa(m.row))+float64(m.n)))
		if m.trow > 0 {
			set("B"+itoa(m.trow), "Safety Meetings", m.label+" — الموضوع", strings.Trim(m.topic, " /"))
		}
	}
	// alerts + comments
	na := 0
	for _, r := range h.recsOn("alert", date) {
		na += maxInt(1, int(r.N("count")))
	}
	set("L67", "Alerts", "Safety Alerts (اليوم)", itoa(na))
	set("O67", "Alerts", "Safety Alerts (الشهر)", ftoa(cb("O67")+float64(na)))
	var cm []string
	for _, r := range h.recsOn("comment", date) {
		cm = append(cm, r.S("text"))
	}
	for _, r := range h.recsOn("greenday", date) {
		cm = append(cm, "Green Day: "+r.S("topic"))
	}
	for i := 0; i < 4; i++ {
		n, t := "", ""
		if i < len(cm) {
			n, t = itoa(i+1), cm[i]
		}
		set("A"+itoa(68+i), "Comments", "Comment "+itoa(i+1), n)
		set("B"+itoa(68+i), "Comments", "Comment "+itoa(i+1)+" — النص", t)
	}
	// manual overrides win
	for k, v := range in.Ov {
		if reportRefRe.MatchString(k) {
			cells[k] = v
			found := false
			for i := range out {
				if out[i].Cell == k {
					out[i].Value, out[i].Auto, found = v, false, true
				}
			}
			if !found {
				out = append(out, pv{k, k, v, "يدوي", false})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return false })
	return out, cells, warns
}

func dmy2(d string) string { return dmy(d) }
func init()                { _ = dmy2 }

func (a *app) dailyHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/daily/preview", guard(func(w http.ResponseWriter, r *http.Request) {
		var in dailyIn
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || in.Date == "" {
			http.Error(w, "bad request", 400)
			return
		}
		if in.BaseID == "" {
			in.BaseID, _ = a.bestBase(in.Date)
		}
		pvs, cells, warns := a.computeDaily(in)
		writeJSON(w, map[string]interface{}{"preview": pvs, "cells": cells, "warnings": warns, "baseId": in.BaseID})
	}))
	mux.HandleFunc("/api/hse/daily/build", guard(func(w http.ResponseWriter, r *http.Request) {
		var in dailyIn
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || in.Date == "" {
			http.Error(w, "bad request", 400)
			return
		}
		name, data, err := a.buildDailyFile(in)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if !lastTrOK {
			w.Header().Set("X-Tr-Warn", "1")
		}
		a.sendFile(w, name, data)
	}))
}

var lastTrOK = true

func (a *app) buildDailyFile(in dailyIn) (string, []byte, error) {
	if in.BaseID == "" {
		in.BaseID, _ = a.bestBase(in.Date)
	}
	_, cells, _ := a.computeDaily(in)
	lastTrOK = true
	{ // free text typed by the user -> language of the Sonatrach report
		var keys, vals []string
		for _, k := range []string{"B7", "B22", "B57", "B59", "B62"} {
			if strings.TrimSpace(cells[k]) != "" {
				keys, vals = append(keys, k), append(vals, cells[k])
			}
		}
		res, ok := a.translateTexts(vals, a.reportLang())
		lastTrOK = ok
		for i, k := range keys {
			cells[k] = res[i]
		}
	}
	sheet := ""
	if t, err := time.Parse("2006-01-02", in.Date); err == nil {
		sheet = t.Format("02-01-06")
	}
	data, err := a.fillReport(in.BaseID, cells, sheet)
	if err != nil {
		return "", nil, err
	}
	rig, well := a.rigWell()
	_, _ = rig, well
	name := a.outName("daily", in.Date, cells["C3"])
	a.hub.mu.Lock()
	a.hub.h.Reports[in.Date] = map[string]interface{}{"cells": cells}
	_ = a.hub.saveLocked()
	a.hub.mu.Unlock()
	return name, data, nil
}

// bestBase: the report with the latest date strictly before `date` (else the newest report).
func (a *app) bestBase(date string) (string, string) {
	bestID, bestD := "", ""
	newID, newD := "", ""
	for _, p := range a.reportTemplates() {
		id := filepath.Base(filepath.Dir(p)) + "/" + filepath.Base(p)
		c, _, ok := a.readBase(id)
		if !ok {
			continue
		}
		t, ok := fromSerial(c["N2"])
		if !ok {
			continue
		}
		d := t.Format("2006-01-02")
		if d < date && d > bestD {
			bestID, bestD = id, d
		}
		if d > newD {
			newID, newD = id, d
		}
	}
	if bestID == "" {
		return newID, newD
	}
	return bestID, bestD
}
