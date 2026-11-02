package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func obj(props map[string]interface{}, req ...string) map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}

var strT = map[string]interface{}{"type": "string"}

func (a *app) modelRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state == "running"
}

func (a *app) aiHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	h := a.hub
	mux.HandleFunc("/api/hse/ai/status", guard(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		st, em, el := a.state, a.errMsg, 0
		if st == "loading" {
			el = int(time.Since(a.started).Seconds())
		}
		a.mu.Unlock()
		writeJSON(w, map[string]interface{}{"running": st == "running", "state": st, "error": em, "elapsed": el})
	}))
	// the user approved starting the model from the confirmation dialog
	mux.HandleFunc("/api/hse/ai/start", guard(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.consent = true
		a.mu.Unlock()
		if !a.modelRunning() {
			go a.ensureModel()
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	mux.HandleFunc("/api/hse/ai/stop", guard(func(w http.ResponseWriter, r *http.Request) {
		a.stopModel()
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	// incident written in any language -> French fields of the Sonatrach template
	mux.HandleFunc("/api/hse/ai/incident", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&in) != nil || strings.TrimSpace(in.Text) == "" {
			http.Error(w, "bad request", 400)
			return
		}
		if err := a.ensureModel(); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		sch := obj(map[string]interface{}{"lieu": strT, "lieudesc": strT, "event": strT, "persons": strT, "env": strT, "assets": strT,
			"kind": map[string]interface{}{"type": "string", "enum": []string{"LTI", "MTC", "FAC", "INC", "NM", "ENV", "OTHER", "FALL"}},
			"time": strT, "uncertain": map[string]interface{}{"type": "array", "items": strT}},
			"lieu", "lieudesc", "event", "persons", "env", "assets", "kind", "time", "uncertain")
		sys := "Tu es un assistant HSE sur un chantier de forage. Le texte de l'utilisateur décrit un accident/incident, dans n'importe quelle langue. " +
			"Traduis et reformule en FRANÇAIS professionnel, sans inventer aucun fait: n'ajoute que ce qui est écrit. Champs: lieu (lieu exact), lieudesc (installations/équipements impliqués), event (brève description chronologique de l'événement), " +
			"persons (conséquences aux personnes, ou \"Néant\"), env (conséquences à l'environnement, ou \"Néant\"), assets (conséquences aux biens, ou \"Néant\"), kind (LTI=accident avec arrêt, MTC=soins médicaux, FAC=premiers soins, INC=incident, NM=presque accident, ENV=déversement/environnement, FALL=chute de hauteur, OTHER), time (HH:MM si mentionnée sinon \"\"). " +
			"uncertain: liste des points que tu as devinés ou qui sont ambigus (à faire vérifier par l'utilisateur)."
		if l := a.reportLang(); l != "fr" {
			sys += " IMPORTANT: this report goes out in " + trLangs[l] + ": write all text fields in professional " + trLangs[l] + " instead of French."
		}
		out, err := a.llmJSON(r.Context(), sys, in.Text, sch, 900)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "فشل المساعد: " + err.Error()})
			return
		}
		var res map[string]interface{}
		if json.Unmarshal([]byte(extractJSONObject(out)), &res) != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "ردّ غير مفهوم من النموذج — أعد المحاولة"})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "fields": res, "review": true})
	}))
	// generic translation
	mux.HandleFunc("/api/hse/ai/translate", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text, To string
			High     bool
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&in) != nil || strings.TrimSpace(in.Text) == "" {
			http.Error(w, "bad request", 400)
			return
		}
		if trLangs[in.To] == "" {
			in.To = "en"
		}
		res, ok := a.translateDetailed([]string{in.Text}, in.To, in.High || a.setting("trQuality") == "high")
		if !ok && a.modelOffNow() {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "MODEL_OFF"})
			return
		}
		if !ok {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "تعذّرت الترجمة: شغّل النموذج (الحالة أعلى الصفحة) أو أعد المحاولة", "from": detectLang(in.Text)})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "text": res[0].Text, "how": res[0].How, "from": detectLang(in.Text), "to": in.To, "review": true})
	}))
	// the user corrected a translation: remember it (wins over the model next time)
	mux.HandleFunc("/api/hse/tr/learn", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Src, To, Text string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&in) != nil || strings.TrimSpace(in.Src) == "" || strings.TrimSpace(in.Text) == "" || trLangs[in.To] == "" {
			http.Error(w, "bad request", 400)
			return
		}
		a.trLoad()
		segs, _ := trSegments(in.Src)
		trMu.Lock()
		if len(segs) <= 1 {
			trMem[tmKey(in.To, in.Src)] = tmEntry{T: strings.TrimSpace(in.Text), U: true, N: time.Now().Unix()}
		}
		trMu.Unlock()
		a.trSave()
		writeJSON(w, map[string]interface{}{"ok": true, "whole": len(segs) <= 1})
	}))
	// STOP cards written in weak English: correct wording, keep the meaning
	mux.HandleFunc("/api/hse/ai/stopfix", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Items []struct{ ID, Desc, Action string } `json:"items"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if err := a.ensureModel(); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		clf := h.classifier()
		var res []map[string]interface{}
		for _, it := range in.Items {
			sch := obj(map[string]interface{}{"desc": strT, "action": strT}, "desc", "action")
			out, err := a.llmJSON(r.Context(), "You correct STOP observation cards written on an oil rig by people with limited English. Fix spelling and grammar and make each sentence clear and professional, in English. Keep the exact meaning; never add facts, names or numbers. If the text is already fine, return it unchanged.",
				"Observation: "+it.Desc+"\nAction taken: "+it.Action, sch, 400)
			item := map[string]interface{}{"id": it.ID, "desc": it.Desc, "action": it.Action, "changed": false}
			var o struct{ Desc, Action string }
			if err == nil && json.Unmarshal([]byte(extractJSONObject(out)), &o) == nil && strings.TrimSpace(o.Desc) != "" {
				item["desc"], item["action"] = o.Desc, firstOf(o.Action, it.Action)
				item["changed"] = o.Desc != it.Desc || o.Action != it.Action
			}
			c := StopCard{Desc: item["desc"].(string), Action: item["action"].(string)}
			n, cl, rk, cf := clf.classify(c)
			item["nature"], item["class"], item["risk"], item["conf"] = n, cl, rk, cf
			res = append(res, item)
		}
		writeJSON(w, map[string]interface{}{"ok": true, "items": res, "review": true})
	}))
	// OCR text correction by the model: only obvious recognition errors, same language, never translate
	mux.HandleFunc("/api/hse/ocr/fix", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || strings.TrimSpace(in.Text) == "" {
			http.Error(w, "bad request", 400)
			return
		}
		if err := a.ensureModel(); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		var outs []string
		for _, seg := range ocrSegments(in.Text, 500) {
			sch := obj(map[string]interface{}{"text": strT}, "text")
			out, err := a.llmJSON(r.Context(), "You repair text produced by OCR (Arabic, French or English, possibly mixed, in HSE / oil-drilling documents). Fix only obvious recognition errors: wrong or missing letters, broken accents, stray symbols, split or merged words. Keep the same language, line breaks and meaning; never translate, never add or remove information, never invent numbers. If unsure, leave the text unchanged.", seg, sch, 700)
			var o struct{ Text string }
			if err == nil && json.Unmarshal([]byte(extractJSONObject(out)), &o) == nil && strings.TrimSpace(o.Text) != "" && len([]rune(o.Text)) > len([]rune(seg))/2 && len([]rune(o.Text)) < len([]rune(seg))*2 {
				outs = append(outs, strings.TrimSpace(o.Text))
			} else {
				outs = append(outs, seg)
			}
		}
		writeJSON(w, map[string]interface{}{"ok": true, "text": strings.Join(outs, "\n"), "review": true})
	}))
	// edit existing cards (user corrections) -> verified, the classifier retrains
	mux.HandleFunc("/api/hub/stop/update", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Cards []StopCard `json:"cards"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		n := 0
		h.mu.Lock()
		for _, c := range in.Cards {
			for i := range h.h.Stops {
				if h.h.Stops[i].ID == c.ID {
					src := h.h.Stops[i].Source
					normStop(&c)
					c.Source, c.Verified = src, true
					h.h.Stops[i] = c
					n++
				}
			}
		}
		h.ver++
		_ = h.saveLocked()
		h.mu.Unlock()
		writeJSON(w, map[string]interface{}{"ok": true, "updated": n})
	}))
}

// ocrSegments splits text into pieces of at most n runes on line breaks.
func ocrSegments(s string, n int) []string {
	var out []string
	cur := ""
	for _, l := range strings.Split(s, "\n") {
		if len([]rune(cur))+len([]rune(l)) > n && cur != "" {
			out = append(out, cur)
			cur = ""
		}
		if cur != "" {
			cur += "\n"
		}
		cur += l
	}
	if strings.TrimSpace(cur) != "" {
		out = append(out, cur)
	}
	return out
}
