package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// llmJSON asks the running local model for a JSON answer constrained by a schema.
func (a *app) llmJSON(ctx context.Context, sys, user string, schema map[string]interface{}, maxTok int) (string, error) {
	return a.llmJSONx(ctx, []map[string]string{{"role": "system", "content": sys}, {"role": "user", "content": user}}, schema, maxTok, 0.7)
}

// llmJSONx: full message list (few-shot turns) and an explicit temperature.
func (a *app) llmJSONx(ctx context.Context, msgs []map[string]string, schema map[string]interface{}, maxTok int, temp float64) (string, error) {
	if err := a.ensureModel(); err != nil {
		return "", err
	}
	a.mu.Lock()
	state, port, noThink := a.state, a.port, a.noThink
	a.mu.Unlock()
	if state != "running" {
		return "", fmt.Errorf("model not running")
	}
	body := map[string]interface{}{
		"model": "local", "stream": false, "cache_prompt": true, "temperature": temp, "max_tokens": maxTok,
		"messages":        msgs,
		"response_format": map[string]interface{}{"type": "json_schema", "json_schema": map[string]interface{}{"name": "out", "schema": schema}},
	}
	if noThink {
		body["chat_template_kwargs"] = map[string]interface{}{"enable_thinking": false}
	}
	bb, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/chat/completions", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var o struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(b, &o) != nil || len(o.Choices) == 0 {
		return "", fmt.Errorf("empty")
	}
	var tf thinkFilter
	return tf.feed(o.Choices[0].Message.Content) + tf.flush(), nil
}

type genReq struct {
	Activity  string   `json:"activity"`
	N         int      `json:"n"`
	Observers []string `json:"observers"` // person IDs
	From      string   `json:"from"`
	To        string   `json:"to"`
	Company   string   `json:"company"`
	Unsafe    int      `json:"unsafe"` // desired % of unsafe cards, -1 = like the dictionary
	AI        bool     `json:"ai"`
}

func (a *app) generateStops(ctx context.Context, rq genReq) ([]StopCard, string) {
	n := rq.N
	if n < 1 {
		n = 10
	}
	if n > 60 {
		n = 60
	}
	var pool []scoredStop
	mode := ""
	switch {
	case strings.TrimSpace(rq.Activity) == "":
		mode = "random"
		a.hub.mu.Lock()
		all := append([]StopCard(nil), a.hub.h.Stops...)
		a.hub.mu.Unlock()
		rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
		for _, c := range all {
			if c.Nature != "" {
				pool = append(pool, scoredStop{c: c})
			}
		}
	case isGeneralReq(rq.Activity):
		mode = "general"
		g := a.generalStops()
		rand.Shuffle(len(g), func(i, j int) { g[i], g[j] = g[j], g[i] })
		for _, c := range g {
			pool = append(pool, scoredStop{c: c})
		}
		rq.Activity = "general site housekeeping: cleanliness, tidiness, storage, PPE and uniform, vehicle parking, camp hygiene — NOT related to drilling operations"
	default:
		pool = a.hub.relevantStops(rq.Activity, "", n*4)
		if len(pool) > n {
			rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		}
	}
	var picked []StopCard
	for _, s := range pool {
		if len(picked) >= n {
			break
		}
		c := s.c
		c.Source = "dict"
		picked = append(picked, c)
	}
	nDict := len(picked)
	nAI, nGen := 0, 0
	// the dictionary has too few cards for this activity: let the model write the rest
	if len(picked) < n && rq.AI {
		if err := a.ensureModel(); err == errModelOff {
			return nil, "MODEL_OFF"
		}
		need := n - len(picked)
		ex := pool
		if len(ex) > 4 {
			ex = ex[:4]
		}
		var exs strings.Builder
		for _, e := range ex {
			fmt.Fprintf(&exs, "- %s => action: %s | %s | %s | %s\n", e.c.Desc, e.c.Action, e.c.Nature, e.c.Class, e.c.Risk)
		}
		if exs.Len() == 0 {
			a.hub.mu.Lock()
			for i := 0; i < 4 && len(a.hub.h.Stops) > 0; i++ {
				e := a.hub.h.Stops[rand.Intn(len(a.hub.h.Stops))]
				fmt.Fprintf(&exs, "- %s => action: %s | %s | %s | %s\n", e.Desc, e.Action, e.Nature, e.Class, e.Risk)
			}
			a.hub.mu.Unlock()
		}
		schema := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"cards": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object",
			"properties": map[string]interface{}{"desc": map[string]string{"type": "string"}, "action": map[string]string{"type": "string"}, "nature": map[string]interface{}{"type": "string", "enum": stopNatures[:3]},
				"class": map[string]interface{}{"type": "string", "enum": stopClasses}, "risk": map[string]interface{}{"type": "string", "enum": []string{"High", "Medium", "Low"}}, "zone": map[string]string{"type": "string"}},
			"required": []string{"desc", "action", "nature", "class", "risk"}}}}, "required": []string{"cards"}}
		sys := "You write realistic STOP (Safety Training Observation Program) cards for a land drilling rig. Each card is one short, concrete observation (1 sentence, plain English like the examples) made during the given activity, with the immediate action taken. Mix unsafe acts, unsafe conditions and a few positive observations. Do not repeat the examples. Reply only with JSON."
		user := fmt.Sprintf("Activity: %s\nWrite %d different cards.\nExamples of the style (observation => action | nature | class | risk):\n%s", rq.Activity, need, exs.String())
		if txt, err := a.llmJSON(ctx, sys, user, schema, 160*need+200); err == nil {
			var o struct {
				Cards []StopCard `json:"cards"`
			}
			if json.Unmarshal([]byte(extractJSONObject(txt)), &o) == nil {
				seen := map[string]bool{}
				for _, c := range picked {
					seen[lc(c.Desc)] = true
				}
				for _, c := range o.Cards {
					if strings.TrimSpace(c.Desc) == "" || seen[lc(c.Desc)] || len(picked) >= n {
						continue
					}
					seen[lc(c.Desc)] = true
					c.Source = "ai"
					c.Status = "Closed"
					picked = append(picked, c)
					nAI++
				}
			}
		}
	}
	// still short: pad with general cards from the dictionary (flagged)
	if len(picked) < n && mode != "general" {
		a.hub.mu.Lock()
		all := append([]StopCard(nil), a.hub.h.Stops...)
		a.hub.mu.Unlock()
		rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
		seen := map[string]bool{}
		for _, c := range picked {
			seen[lc(c.Desc)] = true
		}
		for _, c := range all {
			if len(picked) >= n {
				break
			}
			if seen[lc(c.Desc)] || c.Nature == "" {
				continue
			}
			seen[lc(c.Desc)] = true
			c.Source = "generic"
			picked = append(picked, c)
			nGen++
		}
	}
	clf := a.hub.classifier()
	// observers / dates
	var people []Person
	a.hub.mu.Lock()
	want := map[string]bool{}
	for _, id := range rq.Observers {
		want[id] = true
	}
	for _, p := range a.hub.h.People {
		if want[p.ID] {
			people = append(people, p)
		}
	}
	a.hub.mu.Unlock()
	d0, e0 := time.Parse("2006-01-02", rq.From)
	d1, e1 := time.Parse("2006-01-02", rq.To)
	if e0 != nil {
		d0 = time.Now()
	}
	if e1 != nil || d1.Before(d0) {
		d1 = d0
	}
	span := int(d1.Sub(d0).Hours()/24) + 1
	for i := range picked {
		c := &picked[i]
		c.ID = ""
		if c.Nature == "" || c.Class == "" || c.Risk == "" {
			nn, cc, rr, _ := clf.classify(*c)
			if c.Nature == "" {
				c.Nature = nn
			}
			if c.Class == "" {
				c.Class = cc
			}
			if c.Risk == "" {
				c.Risk = rr
			}
		}
		if c.Risk == "" && c.Nature != "Positive observation" {
			c.Risk = "Medium"
		}
		if c.Zone == "" {
			c.Zone = "Well site"
		}
		c.Date = d0.AddDate(0, 0, i*span/len(picked)).Format("2006-01-02")
		if len(people) > 0 {
			p := people[i%len(people)]
			c.Observer, c.Function, c.Company = p.Name, p.Function, p.Company
		} else if rq.Company != "" {
			c.Company = rq.Company
		}
		if c.Status == "" {
			c.Status = "Closed"
		}
		if c.Action == "" && c.Nature == "Positive observation" {
			c.Action = "Good practice"
		}
		normStop(c)
	}
	// order by date then keep as is
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].Date < picked[j].Date })
	info := fmt.Sprintf("%d من قاموس بطاقاتك", nDict)
	if nAI > 0 {
		info += fmt.Sprintf(" · %d كتبها النموذج (راجعها)", nAI)
	}
	if nGen > 0 {
		info += fmt.Sprintf(" · %d بطاقة عامة غير مرتبطة بالنشاط (القاموس لا يملك ما يكفي)", nGen)
	}
	return picked, info
}

func (a *app) stopHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	h := a.hub
	mux.HandleFunc("/api/hub/stops", guard(func(w http.ResponseWriter, r *http.Request) {
		q := lc(r.URL.Query().Get("q"))
		comp := r.URL.Query().Get("company")
		date := r.URL.Query().Get("date")
		h.mu.Lock()
		defer h.mu.Unlock()
		stat := map[string]map[string]int{"nature": {}, "class": {}, "risk": {}, "status": {}, "company": {}}
		var list []StopCard
		for i := len(h.h.Stops) - 1; i >= 0; i-- {
			c := h.h.Stops[i]
			if comp != "" && c.Company != comp {
				continue
			}
			if date != "" && c.Date != date {
				continue
			}
			stat["nature"][c.Nature]++
			stat["class"][c.Class]++
			stat["risk"][c.Risk]++
			stat["status"][c.Status]++
			stat["company"][c.Company]++
			if q != "" && !strings.Contains(lc(c.Desc+" "+c.Action+" "+c.Observer+" "+c.Zone), q) {
				continue
			}
			if len(list) < 300 || date != "" {
				list = append(list, c)
			}
		}
		writeJSON(w, map[string]interface{}{"total": len(h.h.Stops), "stats": stat, "cards": list})
	}))
	mux.HandleFunc("/api/hub/stop/import", guard(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Query().Get("name"))
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".xls" && ext != ".xlsx" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "الملف يجب أن يكون .xls أو .xlsx"})
			return
		}
		tmp, err := os.CreateTemp("", "stop*"+ext)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		defer os.Remove(tmp.Name())
		_, err = io.Copy(tmp, http.MaxBytesReader(w, r.Body, 80<<20))
		tmp.Close()
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		cards, format, err := importStopFile(tmp.Name(), r.URL.Query().Get("company"))
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		add, dup := h.addStops(h.cleanBatch(cards))
		writeJSON(w, map[string]interface{}{"ok": true, "format": format, "read": len(cards), "added": add, "dup": dup})
	}))
	mux.HandleFunc("/api/hub/stop/save", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Cards []StopCard `json:"cards"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		for i := range in.Cards {
			in.Cards[i].Source = "mine"
			if in.Cards[i].Scheme == "" {
				in.Cards[i].Scheme = firstOf(a.setting("stopScheme"), "sonatrach")
			}
			normStop(&in.Cards[i])
		}
		add, dup := h.addStops(in.Cards)
		writeJSON(w, map[string]interface{}{"ok": true, "added": add, "dup": dup})
	}))
	mux.HandleFunc("/api/hub/stop/delete", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []string `json:"ids"`
			All bool     `json:"all"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in)
		del := map[string]bool{}
		for _, id := range in.IDs {
			del[id] = true
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		var keep []StopCard
		for _, c := range h.h.Stops {
			if !in.All && !del[c.ID] {
				keep = append(keep, c)
			}
		}
		h.h.Stops = keep
		_ = h.saveLocked()
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	mux.HandleFunc("/api/hub/stop/classify", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Items  []StopCard `json:"items"`
			Scheme string     `json:"scheme"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		clf := h.classifierFor(in.Scheme)
		var out []map[string]interface{}
		for _, c := range in.Items {
			orig := c.Desc
			c.Desc, _ = fixText(c.Desc, nil)
			n, cl, rk, cf := clf.classify(c)
			out = append(out, map[string]interface{}{"nature": n, "class": cl, "risk": rk, "conf": cf, "fixed": c.Desc, "changed": c.Desc != strings.TrimSpace(orig)})
		}
		writeJSON(w, map[string]interface{}{"results": out, "trained": clf.n})
	}))
	mux.HandleFunc("/api/hub/stop/generate", guard(func(w http.ResponseWriter, r *http.Request) {
		var rq genReq
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&rq) != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "صف النشاط (مثلاً: عملية logging)"})
			return
		}
		cards, info := a.generateStops(r.Context(), rq)
		if info == "MODEL_OFF" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "MODEL_OFF"})
			return
		}
		if len(cards) == 0 {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "القاموس فارغ — استورد سجلات STOP أولاً"})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "cards": cards, "info": info})
	}))
	mux.HandleFunc("/api/hub/stop/export", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Format string     `json:"format"` // sonatrach | osco
			Title  string     `json:"title"`
			Cards  []StopCard `json:"cards"`
			Lang   string     `json:"lang"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in) != nil || len(in.Cards) == 0 {
			http.Error(w, "bad request", 400)
			return
		}
		lang := in.Lang
		if trLangs[lang] == "" {
			lang = a.reportLang()
		}
		in.Cards, _ = a.trStops(in.Cards, lang)
		var data []byte
		day := time.Now().Format("2006-01-02")
		for _, c := range in.Cards {
			if c.Date != "" {
				day = c.Date
				break
			}
		}
		name := a.outName("stop", day, "")
		if in.Format == "osco" {
			data, name = buildStopOSCO(in.Cards, in.Title), a.outName("stopreg", day, "")
		} else {
			data = buildStopSonatrach(in.Cards, in.Title)
		}
		a.sendFile(w, name, data)
	}))
}
