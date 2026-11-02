package main

// Operations records (daily ops, PTW, trainings, drills, inspections, incidents, Green Day, meetings)
// stored in the hub, photos and the per-day archive folders in the PC's Documents.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed data/trainings.json
var trainingsJSON []byte

type Rec map[string]interface{}

func (r Rec) S(k string) string {
	switch v := r[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
	}
	return ""
}
func (r Rec) N(k string) float64 {
	switch v := r[k].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(strings.Replace(v, ",", ".", 1)), 64)
		return f
	}
	return 0
}
func (r Rec) B(k string) bool {
	switch v := r[k].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}
func (r Rec) L(k string) []string {
	var out []string
	if l, ok := r[k].([]interface{}); ok {
		for _, x := range l {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

var recKinds = map[string]bool{"signaudit": true, "ops": true, "ptw": true, "training": true, "drill": true, "inspection": true, "incident": true,
	"vehicle": true, "driver": true, "company": true, "greenday": true, "meeting": true, "alert": true, "comment": true, "induction": true, "kb": true, "kbprog": true, "checklist": true}

// documents root: <Documents>\UsbAi_HSE (setting "docsDir" overrides)
func (a *app) docsRoot() string {
	a.hub.mu.Lock()
	d := a.hub.h.Settings["docsDir"]
	a.hub.mu.Unlock()
	if d == "" {
		home, _ := os.UserHomeDir()
		docs := filepath.Join(home, "Documents")
		if st, err := os.Stat(docs); err != nil || !st.IsDir() {
			if st2, err2 := os.Stat(filepath.Join(home, "OneDrive", "Documents")); err2 == nil && st2.IsDir() {
				docs = filepath.Join(home, "OneDrive", "Documents")
			} else if os.Getenv("USBAI_ROOT") != "" {
				docs = filepath.Join(a.rootDir, "Documents")
			}
		}
		d = filepath.Join(docs, "UsbAi_HSE")
	}
	if err := os.MkdirAll(d, 0o755); err != nil { // read-only PC: fall back to the drive
		d = filepath.Join(a.rootDir, "Documents", "UsbAi_HSE")
		_ = os.MkdirAll(d, 0o755)
	}
	return d
}

func dmy(d string) string { // 2026-09-17 -> 17-09-2026
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("02-01-2006")
}

func (a *app) dayDir(date string) string {
	d := filepath.Join(a.docsRoot(), dayRel(date))
	_ = os.MkdirAll(d, 0o755)
	return d
}

func (h *hubStore) recsOf(kind string) []Rec {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Rec, len(h.h.Recs[kind]))
	copy(out, h.h.Recs[kind])
	return out
}

func (h *hubStore) recsOn(kind, date string) []Rec {
	var out []Rec
	for _, r := range h.recsOf(kind) {
		if r.S("date") == date {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].S("time") < out[j].S("time") })
	return out
}

func (h *hubStore) recsMonth(kind, date string) []Rec { // same month, up to and including date
	var out []Rec
	for _, r := range h.recsOf(kind) {
		d := r.S("date")
		if len(d) >= 7 && d[:7] == date[:7] && d <= date {
			out = append(out, r)
		}
	}
	return out
}

func (h *hubStore) saveRec(kind string, r Rec) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := r.S("id")
	if id == "" {
		id = newID()
		r["id"] = id
	}
	if r.S("date") == "" {
		r["date"] = time.Now().Format("2006-01-02")
	}
	l := h.h.Recs[kind]
	for i := range l {
		if l[i].S("id") == id {
			l[i] = r
			h.h.Recs[kind] = l
			_ = h.saveLocked()
			return id
		}
	}
	h.h.Recs[kind] = append(l, r)
	_ = h.saveLocked()
	return id
}

func (h *hubStore) delRec(kind, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var keep []Rec
	for _, r := range h.h.Recs[kind] {
		if r.S("id") != id {
			keep = append(keep, r)
		}
	}
	h.h.Recs[kind] = keep
	_ = h.saveLocked()
}

func (a *app) setting(k string) string {
	a.hub.mu.Lock()
	defer a.hub.mu.Unlock()
	return a.hub.h.Settings[k]
}

// reminders for a given day
func (a *app) reminders(date string) []map[string]interface{} {
	t, _ := time.Parse("2006-01-02", date)
	var out []map[string]interface{}
	add := func(id, ar string, done bool, tab string) {
		out = append(out, map[string]interface{}{"id": id, "text": ar, "done": done, "tab": tab})
	}
	add("ops", "إدخال عمليات الحفر لهذا اليوم (Current Drilling Ops)", len(a.hub.recsOn("ops", date)) > 0, "today")
	add("inspection", "التفتيش اليومي (Inspection)", len(a.hub.recsOn("inspection", date)) > 0, "inspect")
	add("training", "التدريب اليومي (Training)", len(a.hub.recsOn("training", date))+len(a.hub.recsOn("drill", date)) > 0, "training")
	if t.Weekday() == time.Friday {
		add("greenday", "🌿 الجمعة: يوم أخضر (Green Day) — نشاط بيئي/سلامة مع صور", len(a.hub.recsOn("greenday", date)) > 0, "green")
	}
	nops := 0
	for _, o := range a.hub.recsOn("ops", date) {
		if o.S("jsa") == "" || o.S("meetingTopic") == "" {
			nops++
		}
	}
	if nops > 0 {
		add("opsjsa", fmt.Sprintf("%d عملية بلا JSA/موضوع اجتماع سلامة (كل عملية تحتاج اجتماعاً وJSA)", nops), false, "today")
	}
	if n, l := a.openPTWReminders(date); n > 0 {
		if len(l) > 3 {
			l = l[:3]
		}
		add("ptwopen", fmt.Sprintf("⚠ %d تصريح PTW لم يُغلق بعد انتهاء وقته (%s) — أغلقه أو مدّد مدته", n, strings.Join(l, "، ")), false, "ptw")
	}
	if n, l := a.docExpiry(date); n > 0 {
		if len(l) > 4 {
			l = append(l[:4], "…")
		}
		add("docsexp", fmt.Sprintf("🚗 %d وثيقة مركبة/سائق منتهية أو قاربت على الانتهاء: %s", n, strings.Join(l, " | ")), false, "people")
	}
	out = append(out, a.chkDue(date)...)
	return out
}

// docExpiry lists vehicle / driver documents that are expired or expire within the alert window
// (setting docAlertDays, default 30). Inactive vehicles and drivers are ignored.
func (a *app) docExpiry(date string) (int, []string) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, nil
	}
	win := 30
	if v, e := strconv.Atoi(a.setting("docAlertDays")); e == nil && v > 0 {
		win = v
	}
	n := 0
	var out []string
	scan := func(kind, label string) {
		for _, r := range a.hub.recsOf(kind) {
			if r.S("active") == "false" {
				continue
			}
			docs, _ := r["docs"].([]interface{})
			for _, d := range docs {
				m, ok := d.(map[string]interface{})
				if !ok {
					continue
				}
				ex, _ := m["expiry"].(string)
				e, err := time.Parse("2006-01-02", strings.TrimSpace(ex))
				if err != nil {
					continue
				}
				days := int(e.Sub(t).Hours() / 24)
				if days > win {
					continue
				}
				n++
				nm, _ := m["name"].(string)
				st := fmt.Sprintf("%dي", days)
				if days < 0 {
					st = "منتهية"
				}
				out = append(out, fmt.Sprintf("%s %s — %s (%s)", label, r.S("name"), nm, st))
			}
		}
	}
	scan("vehicle", "")
	scan("driver", "سائق")
	return n, out
}

func (a *app) opsHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	h := a.hub
	mux.HandleFunc("/api/hse/recs", guard(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		kind := q.Get("kind")
		if !recKinds[kind] {
			http.Error(w, "kind", 400)
			return
		}
		from, to, date := q.Get("from"), q.Get("to"), q.Get("date")
		var out []Rec
		for _, x := range h.recsOf(kind) {
			d := x.S("date")
			if date != "" && d != date || from != "" && d < from || to != "" && d > to {
				continue
			}
			out = append(out, x)
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].S("date") != out[j].S("date") {
				return out[i].S("date") > out[j].S("date")
			}
			return out[i].S("time") > out[j].S("time")
		})
		if out == nil {
			out = []Rec{}
		}
		writeJSON(w, map[string]interface{}{"recs": out})
	}))
	mux.HandleFunc("/api/hse/rec/save", guard(func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("kind")
		var rec Rec
		if !recKinds[kind] || json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&rec) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if kind == "ptw" {
			a.ptwSerial(rec)
		}
		id := h.saveRec(kind, rec)
		go a.afterSave(kind)
		writeJSON(w, map[string]interface{}{"ok": true, "id": id, "rec": rec})
	}))
	mux.HandleFunc("/api/hse/rec/delete", guard(func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("kind")
		if !recKinds[kind] {
			http.Error(w, "kind", 400)
			return
		}
		h.delRec(kind, r.URL.Query().Get("id"))
		go a.afterSave(kind)
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	mux.HandleFunc("/api/hse/photo", guard(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		kind, id := safeName(q.Get("kind")), safeName(q.Get("id"))
		if !recKinds[kind] || id == "" || id == "file" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "bad"})
			return
		}
		dir := filepath.Join(a.docsRoot(), "Photos", kind, id)
		_ = os.MkdirAll(dir, 0o755)
		name := fmt.Sprintf("%d_%s.jpg", time.Now().UnixNano()/1e6, newID()[:4])
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 12<<20))
		if err != nil || len(b) < 100 {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "empty"})
			return
		}
		err = os.WriteFile(filepath.Join(dir, name), b, 0o644)
		writeJSON(w, map[string]interface{}{"ok": err == nil, "file": name})
	}))
	mux.HandleFunc("/api/hse/photo/get", guard(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := filepath.Join(a.docsRoot(), "Photos", safeName(q.Get("kind")), safeName(q.Get("id")), safeName(q.Get("file")))
		w.Header().Set("Cache-Control", "max-age=3600")
		http.ServeFile(w, r, p)
	}))
	mux.HandleFunc("/api/hse/ptw/export", guard(func(w http.ResponseWriter, r *http.Request) {
		rig, well := a.rigWell()
		a.sendFile(w, "PTW_Log.xlsx", buildPTWXlsx(rig, well, h.recsOf("ptw")))
	}))
	mux.HandleFunc("/api/hse/library", guard(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(trainingsJSON)
	}))
	mux.HandleFunc("/api/hse/reminders", guard(func(w http.ResponseWriter, r *http.Request) {
		d := r.URL.Query().Get("date")
		if d == "" {
			d = time.Now().Format("2006-01-02")
		}
		writeJSON(w, map[string]interface{}{"date": d, "items": a.reminders(d), "docs": a.docsRoot()})
	}))
}

// induction: new arrivals are created (or matched) in the people store, put on board and counted.
func (a *app) inductionHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	h := a.hub
	mux.HandleFunc("/api/hse/induction/save", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID      string   `json:"id"`
			Date    string   `json:"date"`
			Time    string   `json:"time"`
			Trainer string   `json:"trainer"`
			Minutes float64  `json:"minutes"`
			Notes   string   `json:"notes"`
			Outline []string `json:"outline"`
			Photos  []string `json:"photos"`
			People  []Person `json:"people"` // new people (ID empty) or existing ones (ID set)
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in) != nil || len(in.People) == 0 {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "أضف شخصاً واحداً على الأقل"})
			return
		}
		if in.Date == "" {
			in.Date = time.Now().Format("2006-01-02")
		}
		when := normWhen(in.Date + " " + firstOf(in.Time, "08:00"))
		var ids []interface{}
		h.mu.Lock()
		for _, p := range in.People {
			p.Name, p.Function, p.Category, p.Company = normSpaces(p.Name), normSpaces(p.Function), normSpaces(p.Category), normSpaces(p.Company)
			if p.Name == "" {
				continue
			}
			found := -1
			for i := range h.h.People {
				if (p.ID != "" && h.h.People[i].ID == p.ID) || (p.ID == "" && lc(h.h.People[i].Name) == lc(p.Name)) {
					found = i
				}
			}
			if found >= 0 {
				q := &h.h.People[found]
				q.Inducted = in.Date
				if !q.onBoardNow() { // returning person: new stay
					q.Stays = append(q.Stays, Stay{In: when})
				}
				ids = append(ids, q.ID)
				continue
			}
			p.ID = newID()
			p.Added = in.Date
			p.Inducted = in.Date
			p.Crew, p.Shift, p.Phone = normCrew(p.Crew), normShift(p.Shift), normSpaces(p.Phone)
			if len(p.Photo) > 60000 || !strings.HasPrefix(p.Photo, "data:image/") {
				p.Photo = ""
			}
			p.Arrive = ""
			p.Stays = []Stay{{In: when, Note: "Induction"}}
			h.h.People = append(h.h.People, p)
			ids = append(ids, p.ID)
		}
		_ = h.saveLocked()
		h.mu.Unlock()
		rec := Rec{"id": in.ID, "date": in.Date, "time": in.Time, "trainer": in.Trainer, "minutes": in.Minutes, "notes": in.Notes,
			"title": "Induction", "people": ids, "count": float64(len(ids)), "photos": in.Photos}
		var ol []interface{}
		for _, o := range in.Outline {
			ol = append(ol, o)
		}
		rec["outline"] = ol
		id := h.saveRec("induction", rec)
		go a.afterSave("induction")
		writeJSON(w, map[string]interface{}{"ok": true, "id": id, "count": len(ids)})
	}))
}

var frMonths = []string{"Janvier", "Fevrier", "Mars", "Avril", "Mai", "Juin", "Juillet", "Aout", "Septembre", "Octobre", "Novembre", "Decembre"}

// dayRel: <year>\<MM-Month>\<dd-mm-yyyy>
func dayRel(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return filepath.Join("Daily", dmy(date))
	}
	return filepath.Join(t.Format("2006"), t.Format("01")+"-"+frMonths[int(t.Month())-1], dmy(date))
}
