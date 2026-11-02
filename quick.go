package main

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---- Quick daily entry: copy yesterday's records, and turn free notes (any language) into records.

type briefItem struct {
	Kind string `json:"kind"`
	Rec  Rec    `json:"rec"`
	Src  string `json:"src"`
}

var reTime = regexp.MustCompile(`\b([01]?\d|2[0-3])[:hH]([0-5]\d)\b`)
var reMin = regexp.MustCompile(`(\d{1,3})\s*(min|mn|minutes?|دقيقه|دقائق|دقيقة)`)
var rePeople = regexp.MustCompile(`(\d{1,3})\s*(persons?|people|personnes?|pax|اشخاص|أشخاص|عامل|عمال|حاضر)`)

func kwAny(n string, ws ...string) bool {
	for _, w := range ws {
		if strings.Contains(n, normQ(w)) {
			return true
		}
	}
	return false
}

func briefKind(n string) string {
	switch {
	case kwAny(n, "incident", "accident", "حادث", "حادثه", "اصابه", "near miss", "presque accident"):
		return "incident"
	case kwAny(n, "safety alert", "alerte securite", "تنبيه سلامه", "تنبيه امني"):
		return "alert"
	case kwAny(n, "drill", "exercice", "simulation", "تمرين", "محاكاه", "مناوره"):
		return "drill"
	case kwAny(n, "meeting", "reunion", "pre job", "prejob", "pre tour", "tbt", "toolbox meeting", "اجتماع", "ملتقي"):
		return "meeting"
	case kwAny(n, "training", "formation", "sensibilis", "toolbox", "induction", "تدريب", "تكوين", "توعيه", "محاضره"):
		return "training"
	case kwAny(n, "inspection", "inspecte", "inspected", "controle", "checked", "check ", "تفتيش", "فحص", "تفقد", "معاينه"):
		return "inspection"
	case kwAny(n, "drilling", "tripping", "trip ", "casing", "tubage", "cement", "logging", "diagraph", "reaming", "circulat", "rig up", "rig down", "bop test", "fishing", "coring", "manoeuvre", "forage", "wiper", "pooh", "rih", "bha", "حفر", "سحب", "انزال", "تسمنت", "اسمنت", "لوغينغ", "دوران", "اختبار bop", "تبطين"):
		return "ops"
	case kwAny(n, "comment", "remarque", "note:", "ملاحظه", "تعليق"):
		return "comment"
	}
	return "comment"
}

func briefArea(n string) string {
	switch {
	case kwAny(n, "camp", "hygiene", "kitchen", "cuisine", "مخيم", "كامب", "نظافه", "مطبخ", "ماكل"):
		return "camp"
	case kwAny(n, "mast", "derrick", "برج", "صاري", "mât"):
		return "mast"
	case kwAny(n, "fire", "extinguisher", "incendie", "extincteur", "طفاي", "حريق", "مطافي"):
		return "fire"
	case kwAny(n, "lifting", "levage", "sling", "رفع", "حبال", "شكل"):
		return "lifting"
	case kwAny(n, "crane", "forklift", "grue", "رافعه", "رافعة"):
		return "crane"
	case kwAny(n, "environment", "environnement", "waste", "dechet", "بيئه", "نفايات"):
		return "env"
	}
	return "rig"
}

func briefDrill(n string) string {
	switch {
	case kwAny(n, "kick", "venue", "ركله"):
		return "kick"
	case kwAny(n, "fire", "incendie", "حريق"):
		return "fire"
	case kwAny(n, "medevac", "medical", "evacuation", "اخلاء", "اسعاف"):
		return "medevac"
	case kwAny(n, "security", "securite", "امن"):
		return "security"
	case kwAny(n, "spill", "deversement", "environment", "تسرب", "بيئ"):
		return "environment"
	}
	return "other"
}

func briefMeeting(n string) string {
	switch {
	case kwAny(n, "weekly", "hebdo", "اسبوعي"):
		return "weekly"
	case kwAny(n, "crew change", "pre tour", "pre-tour", "تغيير الطاقم", "تغيير النوبه"):
		return "crew"
	case kwAny(n, "morning", "matin", "صباحي"):
		return "morning"
	}
	return "prejob"
}

// buildBrief: one proposal (record) from a line of notes and its kind
func (a *app) buildBrief(kind, text, date string) briefItem {
	n := normQ(text)
	text = strings.TrimSpace(text)
	r := Rec{"date": date}
	switch kind {
	case "ops":
		r["text"] = text
		if m := reTime.FindStringSubmatch(text); m != nil {
			h, _ := strconv.Atoi(m[1])
			r["time"] = strconv.Itoa(h/10) + strconv.Itoa(h%10) + ":" + m[2]
		}
	case "training":
		r["title"] = text
		if m := reMin.FindStringSubmatch(n); m != nil {
			v, _ := strconv.Atoi(m[1])
			r["minutes"] = v
		}
		r["trainer"] = a.setting("hseName")
		if m := rePeople.FindStringSubmatch(n); m != nil {
			v, _ := strconv.Atoi(m[1])
			r["count"] = v
		}
	case "inspection":
		r["area"], r["findings"], r["count"], r["inspector"] = briefArea(n), text, 1, a.setting("hseName")
	case "drill":
		r["type"], r["situation"] = briefDrill(n), text
		if m := reMin.FindStringSubmatch(n); m != nil {
			v, _ := strconv.Atoi(m[1])
			r["minutes"] = v
		}
	case "meeting":
		r["mkind"], r["topic"] = briefMeeting(n), text
		if m := rePeople.FindStringSubmatch(n); m != nil {
			v, _ := strconv.Atoi(m[1])
			r["count"] = v
		}
	case "alert":
		r["text"], r["count"] = text, 1
	case "incident":
		r["text"] = text
	default:
		kind = "comment"
		r["text"] = text
	}
	return briefItem{Kind: kind, Rec: r, Src: text}
}

func briefLines(s string) []string {
	var out []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ';' || r == '؛' || r == '•' }) {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*•·–—0123456789.)( \t"))
		if len([]rune(l)) < 4 {
			continue
		}
		out = append(out, l)
	}
	return out
}

func (a *app) quickHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	h := a.hub
	// copy the latest earlier day's records of a kind to `date` (ops / inspection / meeting)
	mux.HandleFunc("/api/hse/copyprev", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Kind, Date string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in) != nil || (in.Kind != "ops" && in.Kind != "inspection" && in.Kind != "meeting") || in.Date == "" {
			http.Error(w, "bad request", 400)
			return
		}
		if len(h.recsOn(in.Kind, in.Date)) > 0 {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "يوجد سجلات لهذا اليوم بالفعل — لن أضيف نسخة مكرّرة"})
			return
		}
		best := ""
		for _, x := range h.recsOf(in.Kind) {
			if d := x.S("date"); d < in.Date && d > best {
				best = d
			}
		}
		if best == "" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "لا يوجد يوم سابق مسجّل لنسخه"})
			return
		}
		n := 0
		for _, x := range h.recsOn(in.Kind, best) {
			c := Rec{}
			for k, v := range x {
				switch k {
				case "id", "photos", "serial", "doc", "created":
					continue
				}
				c[k] = v
			}
			if in.Kind == "inspection" { // the findings are new every day
				c["findings"], c["actions"] = "", ""
			}
			c["date"] = in.Date
			h.saveRec(in.Kind, c)
			n++
		}
		writeJSON(w, map[string]interface{}{"ok": true, "copied": n, "from": best})
	}))
	// notes in any language -> proposed records (the user reviews them before saving)
	mux.HandleFunc("/api/hse/brief", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text, Date string
			AI         bool
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&in) != nil || strings.TrimSpace(in.Text) == "" {
			http.Error(w, "bad request", 400)
			return
		}
		if in.Date == "" {
			in.Date = time.Now().Format("2006-01-02")
		}
		by := "rules"
		var items []briefItem
		if in.AI {
			if err := a.ensureModel(); err == errModelOff {
				writeJSON(w, map[string]interface{}{"ok": false, "error": "MODEL_OFF"})
				return
			}
			sch := obj(map[string]interface{}{"items": map[string]interface{}{"type": "array", "items": obj(map[string]interface{}{
				"kind": map[string]interface{}{"type": "string", "enum": []string{"ops", "training", "inspection", "drill", "meeting", "alert", "comment", "incident"}},
				"text": strT}, "kind", "text")}}, "items")
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
			out, err := a.llmJSONx(ctx, []map[string]string{{"role": "system", "content": "You are the assistant of an HSE officer on an oil drilling rig. The user pastes the day's notes in any language (Arabic, French, English). Split them into separate records. kind: ops = current drilling operation (drilling, tripping, casing, cementing, logging...), training = training/toolbox/awareness session, inspection = inspection or check of an area/equipment, drill = emergency drill, meeting = safety meeting, alert = safety alert, incident = accident/incident/near miss, comment = anything else. text = the record in ONE short sentence, in the same language as the note, faithful to it: never invent facts, names or numbers."}, {"role": "user", "content": in.Text}}, sch, 900, 0.1)
			cancel()
			var res struct {
				Items []struct{ Kind, Text string } `json:"items"`
			}
			if err == nil && json.Unmarshal([]byte(extractJSONObject(out)), &res) == nil && len(res.Items) > 0 {
				by = "model"
				for _, it := range res.Items {
					if strings.TrimSpace(it.Text) != "" {
						items = append(items, a.buildBrief(it.Kind, it.Text, in.Date))
					}
				}
			}
		}
		if len(items) == 0 {
			for _, l := range briefLines(in.Text) {
				items = append(items, a.buildBrief(briefKind(normQ(l)), l, in.Date))
			}
		}
		writeJSON(w, map[string]interface{}{"ok": true, "items": items, "by": by})
	}))
}
