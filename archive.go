package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func copyFile(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer out.Close()
	_, _ = io.Copy(out, in)
}

// archiveDay writes everything of the day into Documents\UsbAi_HSE\Daily\dd-mm-yyyy.
func (a *app) archiveDay(in dailyIn) (string, []string, []string, error) {
	dir := a.dayDir(in.Date)
	var files, warns []string
	put := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, safeName(name)), data, 0o644); err == nil {
			files = append(files, safeName(name))
		}
	}
	rig, well := a.rigWell()
	if in.BaseID == "" {
		in.BaseID, _ = a.bestBase(in.Date)
	}
	if in.BaseID != "" {
		if name, data, err := a.buildDailyFile(in); err == nil {
			put(name, data)
			if !lastTrOK {
				warns = append(warns, "نصوص التقرير اليومي لم تُترجم كلياً (النموذج متوقف أو فشلت الترجمة): راجع اللغة قبل الإرسال.")
			}
		} else {
			warns = append(warns, "تعذّر إنشاء تقرير صوناطراك: "+err.Error())
		}
	} else {
		warns = append(warns, "لا يوجد تقرير سابق كأساس؛ ارفع تقرير صوناطراك .xlsx من تبويب التقارير أولاً.")
	}
	rq := pobReq{Date: in.Date, Rig: rig, Well: well, Phone: true, Shift: true}
	if p := a.pobPeople(rq); len(p) > 0 {
		put(a.outName("pob", in.Date, ""), buildPOBXlsx(groupByCompany(p), rq))
	}
	if cards := a.stopsMonthTo(in.Date); len(cards) > 0 {
		tc, ok := a.trStops(cards, a.reportLang())
		if !ok {
			warns = append(warns, "بطاقات STOP لم تُترجم (النموذج متوقف): تحقق من لغتها قبل الإرسال.")
		}
		put(a.outName("stop", in.Date, ""), buildStopSonatrach(tc, "Stop Card Log  "+in.Date[5:7]+" / "+in.Date[:4]))
	}
	if ptw := a.ptwMonthTo(in.Date); len(ptw) > 0 {
		put(a.outName("ptw", in.Date, ""), buildPTWXlsx(rig, well, ptw))
	}
	for _, kind := range []string{"incident", "drill", "induction", "training", "greenday", "inspection"} {
		for _, r := range a.hub.recsOn(kind, in.Date) {
			if kind == "drill" {
				if ps := a.personNames(r.L("attendees")); len(ps) > 0 {
					var n []interface{}
					for _, p := range ps {
						n = append(n, p.Name)
					}
					r["attendeeNames"] = n
				}
			}
			data, name := a.docFor(kind, r)
			put(name, data)
			for _, f := range r.L("photos") {
				copyFile(filepath.Join(a.docsRoot(), "Photos", kind, safeName(r.S("id")), safeName(f)), filepath.Join(dir, "Photos", kind+"_"+safeName(f)))
			}
		}
	}
	return dir, files, warns, nil
}

func (a *app) archiveHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/day/archive", guard(func(w http.ResponseWriter, r *http.Request) {
		var in dailyIn
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || in.Date == "" {
			http.Error(w, "bad request", 400)
			return
		}
		dir, files, warns, err := a.archiveDay(in)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "dir": dir, "files": files, "warnings": warns})
	}))
	mux.HandleFunc("/api/hse/day/list", guard(func(w http.ResponseWriter, r *http.Request) {
		root := a.docsRoot()
		var days []map[string]interface{}
		_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err != nil || !e.IsDir() || p == root {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if (parts[0] == "People" || parts[0] == "PTW" || parts[0] == "Photos") && len(parts) >= 1 {
				return filepath.SkipDir
			}
			if len(parts) == 1 && !(len(parts[0]) == 4 || parts[0] == "Daily") {
				return filepath.SkipDir
			}
			if strings.EqualFold(e.Name(), "Photos") {
				return filepath.SkipDir
			}
			sub, _ := os.ReadDir(p)
			var fs []string
			for _, s := range sub {
				if !s.IsDir() {
					fs = append(fs, s.Name())
				}
			}
			if len(fs) > 0 && len(parts) >= 2 {
				days = append(days, map[string]interface{}{"day": filepath.ToSlash(rel), "files": fs})
			}
			return nil
		})
		sort.Slice(days, func(i, j int) bool { return days[i]["day"].(string) > days[j]["day"].(string) })
		writeJSON(w, map[string]interface{}{"root": root, "days": days})
	}))
	mux.HandleFunc("/api/hse/day/file", guard(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rel := filepath.Clean("/" + filepath.FromSlash(q.Get("day")))[1:]
		p := filepath.Join(a.docsRoot(), rel, safeName(q.Get("name")))
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+strings.ReplaceAll(safeName(q.Get("name")), " ", "%20"))
		http.ServeFile(w, r, p)
	}))
}
