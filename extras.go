package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *app) extraHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	// ---- backups: list + restore (the current data is saved first, nothing is lost)
	mux.HandleFunc("/api/hse/backup/list", guard(func(w http.ResponseWriter, r *http.Request) {
		dir := filepath.Join(filepath.Dir(a.hub.path), "backup")
		ents, _ := os.ReadDir(dir)
		var out []map[string]interface{}
		for i := len(ents) - 1; i >= 0; i-- {
			e := ents[i]
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, map[string]interface{}{"name": e.Name(), "kb": fi.Size() / 1024, "time": fi.ModTime().Format("2006-01-02 15:04")})
		}
		writeJSON(w, map[string]interface{}{"dir": dir, "items": out})
	}))
	mux.HandleFunc("/api/hse/backup/restore", guard(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Query().Get("name"))
		if !strings.HasPrefix(name, "hse-") || !strings.HasSuffix(name, ".json") {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "اسم غير صالح"})
			return
		}
		dir := filepath.Join(filepath.Dir(a.hub.path), "backup")
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		var test Hub
		if json.Unmarshal(b, &test) != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "الملف تالف"})
			return
		}
		a.hub.mu.Lock()
		defer a.hub.mu.Unlock()
		_ = os.MkdirAll(dir, 0o755)
		if cur, err := os.ReadFile(a.hub.path); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "before-restore-"+time.Now().Format("20060102-150405")+".json.keep"), cur, 0o644)
		}
		if test.Reports == nil {
			test.Reports = map[string]map[string]interface{}{}
		}
		if test.Recs == nil {
			test.Recs = map[string][]Rec{}
		}
		if test.Settings == nil {
			test.Settings = map[string]string{}
		}
		a.hub.h = test
		a.hub.ver++
		if err := a.hub.saveLocked(); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "people": len(test.People), "stops": len(test.Stops)})
	}))

	// ---- salary (days on board x daily wage, or monthly / days of the month)
	mux.HandleFunc("/api/hse/salary", guard(func(w http.ResponseWriter, r *http.Request) {
		rows, tot := a.salaryRows(r.URL.Query().Get("month"), splitCSV(r.URL.Query().Get("companies")))
		writeJSON(w, map[string]interface{}{"rows": rows, "total": tot})
	}))
	mux.HandleFunc("/api/hse/salary/export", guard(func(w http.ResponseWriter, r *http.Request) {
		month := r.URL.Query().Get("month")
		rows, tot := a.salaryRows(month, splitCSV(r.URL.Query().Get("companies")))
		sh := XSheet{Name: "Salary " + month, Widths: map[int]int{0: 30, 1: 22, 2: 20, 3: 10, 4: 12, 5: 14, 6: 16}}
		sh.Rows = append(sh.Rows, []XCell{xt("Name", 7), xt("Function", 7), xt("Company", 7), xt("Days", 7), xt("Daily", 7), xt("Monthly", 7), xt("Amount", 7)})
		for _, x := range rows {
			sh.Rows = append(sh.Rows, []XCell{xt(x["name"].(string), 2), xt(x["function"].(string), 2), xt(x["company"].(string), 2),
				xt(fmt.Sprint(x["days"]), 2), xt(fmt.Sprint(x["daily"]), 2), xt(fmt.Sprint(x["monthly"]), 2), xt(fmt.Sprintf("%.2f", x["amount"]), 2)})
		}
		sh.Rows = append(sh.Rows, []XCell{xt("TOTAL", 7), xt("", 7), xt("", 7), xt("", 7), xt("", 7), xt("", 7), xt(fmt.Sprintf("%.2f", tot), 7)})
		a.sendFile(w, "Salary_"+safeName(month)+".xlsx", buildBook([]XSheet{sh}))
	}))

	// ---- diagnostics: everything a technician needs to know on a new PC
	mux.HandleFunc("/api/hse/diag", guard(func(w http.ResponseWriter, r *http.Request) {
		type chk struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		}
		var cs []chk
		add := func(n string, ok bool, d string) { cs = append(cs, chk{n, ok, d}) }
		free := freeRAMGB()
		add("النظام / المعمارية", true, runtime.GOOS+" / "+runtime.GOARCH)
		add("الذاكرة الحرّة", free >= 1.5, fmt.Sprintf("%.1f GB%s", free, map[bool]string{true: "", false: " — قليلة: أغلق البرامج الأخرى"}[free >= 1.5]))
		a.scan()
		add("محرك llamafile", a.exe != "", firstOf(a.exe, "غير موجود في AI_CORE"))
		var ms []string
		for _, m := range a.models {
			ms = append(ms, fmt.Sprintf("%s (%.2f GB)", m.Name, m.GB))
		}
		add("النماذج", len(ms) > 0, firstOf(strings.Join(ms, " · "), "لا يوجد نموذج في AI_CORE"))
		add("حالة النموذج", true, a.stateName())
		for _, d := range []struct{ n, p string }{{"مجلد البيانات data", filepath.Dir(a.hub.path)}, {"مجلد التقارير", a.docsRoot()}, {"Catalogs", a.catDir()}, {"STOP_Archive", a.stopArchDir()}, {"People", filepath.Dir(a.peopleMasterPath())}} {
			_ = os.MkdirAll(d.p, 0o755)
			tf := filepath.Join(d.p, ".w"+newID())
			err := os.WriteFile(tf, []byte("x"), 0o644)
			_ = os.Remove(tf)
			add(d.n, err == nil, d.p+map[bool]string{true: "", false: " — لا يمكن الكتابة هنا!"}[err == nil])
		}
		rs, cst := a.rag.status(), a.cat.status()
		add("فهرس rag", rs.Exists, fmt.Sprintf("%d ملف · %d مقطع", len(rs.Files), rs.Chunks))
		add("فهرس Catalogs", true, fmt.Sprintf("%d ملف · %d مقطع", len(cst.Files), cst.Chunks))
		add("قاموس STOP", len(a.hub.h.Stops) > 0, fmt.Sprintf("%d بطاقة", len(a.hub.h.Stops)))
		add("قوالب تقرير صوناطراك", len(a.reportTemplates()) > 0, fmt.Sprintf("%d قالب", len(a.reportTemplates())))
		add("الأشخاص (POB)", true, fmt.Sprintf("%d شخصاً", len(a.hub.h.People)))
		bk, _ := os.ReadDir(filepath.Join(filepath.Dir(a.hub.path), "backup"))
		add("النسخ الاحتياطية", len(bk) > 0, fmt.Sprintf("%d نسخة", len(bk)))
		writeJSON(w, map[string]interface{}{"checks": cs})
	}))
	// quick model test: load time + speed (tokens/second) — tells whether the PC is fast enough
	mux.HandleFunc("/api/hse/diag/model", guard(func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		if err := a.ensureModel(); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		load := time.Since(t0).Seconds()
		a.mu.Lock()
		port, noThink := a.port, a.noThink
		a.mu.Unlock()
		body := map[string]interface{}{"model": "local", "stream": false, "max_tokens": 48, "temperature": 0.2,
			"messages": []map[string]string{{"role": "user", "content": "Write one short sentence about wearing a safety helmet."}}}
		if noThink {
			body["chat_template_kwargs"] = map[string]interface{}{"enable_thinking": false}
		}
		bb, _ := json.Marshal(body)
		t1 := time.Now()
		req, _ := http.NewRequestWithContext(r.Context(), "POST", "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/chat/completions", bytes.NewReader(bb))
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var o struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				C int `json:"completion_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(b, &o)
		el := time.Since(t1).Seconds()
		txt := ""
		if len(o.Choices) > 0 {
			txt = strings.TrimSpace(o.Choices[0].Message.Content)
		}
		tps := 0.0
		if o.Usage.C > 0 && el > 0 {
			tps = float64(o.Usage.C) / el
		}
		verdict := "ok"
		if tps > 0 && tps < 2 {
			verdict = "slow"
		}
		writeJSON(w, map[string]interface{}{"ok": resp.StatusCode == 200 && txt != "", "load_s": load, "gen_s": el, "tokens": o.Usage.C, "tps": tps, "text": txt, "verdict": verdict})
	}))
}

func splitCSV(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (a *app) stateName() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == "error" {
		return "خطأ: " + a.errMsg
	}
	switch a.state {
	case "running":
		return "يعمل"
	case "loading":
		return "قيد التحميل"
	}
	return "متوقف"
}

// salaryRows: days on board in the month x daily wage (or monthly pay pro rata when no daily wage is set).
func (a *app) salaryRows(month string, companies []string) ([]map[string]interface{}, float64) {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		t = time.Now()
	}
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	dim := first.AddDate(0, 1, -1).Day()
	people := a.hub.peopleOf(companies, first.Format("2006-01-02")+"T00:00", first.AddDate(0, 1, -1).Format("2006-01-02")+"T23:59")
	var out []map[string]interface{}
	total := 0.0
	for _, p := range people {
		if p.Daily <= 0 && p.Monthly <= 0 {
			continue
		}
		days := 0
		for d := 0; d < dim; d++ {
			day := first.AddDate(0, 0, d).Format("2006-01-02")
			if len(p.Stays) == 0 || p.onBoardBetween(day+"T00:00", day+"T23:59") {
				days++
			}
		}
		amt := 0.0
		if p.Daily > 0 {
			amt = float64(days) * p.Daily
		} else {
			amt = p.Monthly * float64(days) / float64(dim)
		}
		total += amt
		out = append(out, map[string]interface{}{"name": p.Name, "function": p.Function, "company": p.Company, "days": days, "daily": p.Daily, "monthly": p.Monthly, "amount": float64(int(amt*100+0.5)) / 100})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["company"].(string) < out[j]["company"].(string) })
	return out, float64(int(total*100+0.5)) / 100
}

// openPTWReminders: permits still open after their closing time (or from a previous day).
func (a *app) openPTWReminders(date string) (n int, list []string) {
	now := time.Now()
	for _, r := range a.hub.recsOf("ptw") {
		st := strings.ToLower(r.S("status"))
		if st == "closed" || st == "مغلق" {
			continue
		}
		d := r.S("date")
		if d == "" || d > date {
			continue
		}
		late := d < date
		if d == date && date == now.Format("2006-01-02") {
			if e := r.S("end"); e != "" && e < now.Format("15:04") {
				late = true
			}
		}
		if late {
			n++
			list = append(list, firstOf(r.S("serial"), r.S("desc")))
		}
	}
	return
}
