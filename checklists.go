package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Inspection checklist templates (Great Wall Drilling HSE-MS forms, digitised) — see data/checklists.json.
//
//go:embed data/checklists.json
var chkJSON []byte

type chkTitle struct {
	Ar string `json:"ar"`
	Fr string `json:"fr"`
	En string `json:"en"`
}

type chkTpl struct {
	ID     string   `json:"id"`
	Freq   string   `json:"freq"`
	Days   int      `json:"days"`
	Remind bool     `json:"remind"`
	Title  chkTitle `json:"title"`
}

var chkTpls []chkTpl

func init() {
	var w struct {
		T []chkTpl `json:"templates"`
	}
	if json.Unmarshal(chkJSON, &w) == nil {
		chkTpls = w.T
	}
}

// chkRemindOn: per-template override in setting chkRemind ({"id":true|false}); otherwise the template default.
func (a *app) chkRemindOn(t chkTpl) bool {
	var m map[string]bool
	if s := a.setting("chkRemind"); s != "" && json.Unmarshal([]byte(s), &m) == nil {
		if v, ok := m[t.ID]; ok {
			return v
		}
	}
	return t.Remind
}

// chkLast returns the latest completion date per template id and the count.
func (a *app) chkLast() (map[string]string, map[string]int) {
	last, cnt := map[string]string{}, map[string]int{}
	for _, r := range a.hub.recsOf("checklist") {
		id, d := r.S("tpl"), r.S("date")
		cnt[id]++
		if d > last[id] {
			last[id] = d
		}
	}
	return last, cnt
}

// chkDue lists overdue (or never done) templates that the user asked to be reminded about.
func (a *app) chkDue(date string) []map[string]interface{} {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil
	}
	last, _ := a.chkLast()
	var out []map[string]interface{}
	for _, c := range chkTpls {
		if c.Days <= 0 || !a.chkRemindOn(c) {
			continue
		}
		l := last[c.ID]
		txt := ""
		if l == "" {
			txt = fmt.Sprintf("📋 قائمة فحص لم تُنجز بعد: %s (%s)", c.Title.Ar, c.Freq)
		} else if lt, e := time.Parse("2006-01-02", l); e == nil {
			age := int(t.Sub(lt).Hours() / 24)
			if age >= c.Days {
				txt = fmt.Sprintf("📋 قائمة فحص مستحقة: %s — آخر مرة %s (منذ %d يوم)", c.Title.Ar, dmy(l), age)
			}
		}
		if txt != "" {
			out = append(out, map[string]interface{}{"id": "chk-" + c.ID, "text": txt, "done": false, "tab": "chk"})
		}
	}
	return out
}

func (a *app) chkHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/checklists", guard(func(w http.ResponseWriter, r *http.Request) {
		last, cnt := a.chkLast()
		st := map[string]interface{}{}
		today := time.Now().Format("2006-01-02")
		if d := r.URL.Query().Get("date"); d != "" {
			today = d
		}
		now, _ := time.Parse("2006-01-02", today)
		for _, c := range chkTpls {
			e := map[string]interface{}{"last": last[c.ID], "n": cnt[c.ID], "remind": a.chkRemindOn(c)}
			if l := last[c.ID]; l != "" {
				if lt, err := time.Parse("2006-01-02", l); err == nil {
					age := int(now.Sub(lt).Hours() / 24)
					e["age"] = age
					e["due"] = c.Days > 0 && age >= c.Days
				}
			} else {
				e["due"] = c.Days > 0
			}
			st[c.ID] = e
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var raw json.RawMessage = chkJSON
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "tpl": raw, "state": st})
	}))
	mux.HandleFunc("/api/hse/checklists/remind", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
			On bool   `json:"on"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil || strings.TrimSpace(in.ID) == "" {
			http.Error(w, "bad", 400)
			return
		}
		m := map[string]bool{}
		if s := a.setting("chkRemind"); s != "" {
			_ = json.Unmarshal([]byte(s), &m)
		}
		m[in.ID] = in.On
		b, _ := json.Marshal(m)
		a.hub.mu.Lock()
		a.hub.h.Settings["chkRemind"] = string(b)
		a.hub.ver++
		_ = a.hub.saveLocked()
		a.hub.mu.Unlock()
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
}
