package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

//go:embed data/hsekb.json
var kbJSON []byte

type kbRef struct {
	T string `json:"t"`
	U string `json:"u,omitempty"`
}

// accepts both "Title" and {"t":..,"u":..}
func (r *kbRef) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		r.T = s
		return nil
	}
	type x kbRef
	return json.Unmarshal(b, (*x)(r))
}

type kbQuiz struct {
	Q    map[string]string   `json:"q"`
	Opts map[string][]string `json:"opts"`
	A    int                 `json:"a"`
}

type kbEntry struct {
	ID     string              `json:"id"`
	Cat    string              `json:"cat"`
	Sub    string              `json:"sub,omitempty"`
	Type   string              `json:"type,omitempty"`
	Level  string              `json:"level,omitempty"`
	Title  map[string]string   `json:"title"`
	Body   map[string]string   `json:"body"`
	Steps  map[string][]string `json:"steps,omitempty"`
	Tags   []string            `json:"tags"`
	Refs   []kbRef             `json:"refs"`
	Verify bool                `json:"verify"`
	Quiz   *kbQuiz             `json:"quiz,omitempty"`
	Custom bool                `json:"custom,omitempty"`
}

// order of the learning path (categories); glossary is reference only
var kbPathOrder = []string{"app", "role", "ppe", "signs", "hazards", "ptw", "wellcontrol", "drilling", "wellops", "jsa", "toolbox", "inspect", "emergency", "health", "env", "kpi", "system", "legal"}

var kbBuiltin = func() []kbEntry {
	var e []kbEntry
	_ = json.Unmarshal(kbJSON, &e)
	return e
}()

// all entries: the built-in knowledge base + the user's own entries (record kind "kb")
func (a *app) kbEntries() []kbEntry {
	out := append([]kbEntry{}, kbBuiltin...)
	for _, r := range a.hub.recsOf("kb") {
		t := strings.TrimSpace(r.S("title"))
		b := strings.TrimSpace(r.S("body"))
		if t == "" || b == "" {
			continue
		}
		lg := firstOf(r.S("lang"), "ar")
		out = append(out, kbEntry{ID: "U-" + r.S("id"), Cat: firstOf(r.S("cat"), "custom"), Title: map[string]string{lg: t}, Body: map[string]string{lg: b},
			Tags: splitCSV(r.S("tags")), Custom: true})
	}
	return out
}

func kbText(e kbEntry) string {
	var b strings.Builder
	for _, l := range []string{"ar", "fr", "en"} {
		b.WriteString(e.Title[l] + ". " + e.Body[l] + " ")
	}
	for _, st := range e.Steps["en"] {
		b.WriteString(st + " ")
	}
	b.WriteString(strings.Join(e.Tags, " "))
	return b.String()
}

// kbSearcher: BM25 index over the knowledge base (rebuilt when the user's entries change)
func (a *app) kbSearcher() (*searchIndex, []kbEntry) {
	es := a.kbEntries()
	key := len(es)
	for _, e := range es {
		if e.Custom {
			key += len(e.Body["ar"]) + len(e.Body["fr"]) + len(e.Body["en"])
		}
	}
	a.kbMu.Lock()
	defer a.kbMu.Unlock()
	if a.kbIdx != nil && a.kbKey == key {
		return a.kbIdx, a.kbList
	}
	var d indexData
	for i, e := range es {
		d.Files = append(d.Files, fileInfo{Rel: e.ID + " " + firstOf(e.Title["en"], e.Title["fr"], e.Title["ar"]), Status: "ok", Chunks: 1})
		d.Chunks = append(d.Chunks, chunk{File: i, Text: kbText(e)})
	}
	a.kbIdx, a.kbList, a.kbKey = buildSearch(d), es, key
	return a.kbIdx, es
}

func kbItem(e kbEntry, full bool, score float64) map[string]interface{} {
	m := map[string]interface{}{"id": e.ID, "cat": e.Cat, "sub": e.Sub, "type": e.Type, "level": e.Level, "title": e.Title, "tags": e.Tags, "verify": e.Verify, "custom": e.Custom, "score": score}
	if full {
		m["body"], m["steps"], m["refs"], m["quiz"] = e.Body, e.Steps, e.Refs, e.Quiz
	} else {
		pv := map[string]string{}
		for l, b := range e.Body {
			r := []rune(b)
			if len(r) > 140 {
				r = append(r[:140], '…')
			}
			pv[l] = string(r)
		}
		m["preview"] = pv
	}
	return m
}

func (a *app) kbHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/kb/search", guard(func(w http.ResponseWriter, r *http.Request) {
		qv := r.URL.Query()
		q := strings.TrimSpace(qv.Get("q"))
		cat, typ, lvl := qv.Get("cat"), qv.Get("type"), qv.Get("level")
		off, _ := strconv.Atoi(qv.Get("offset"))
		lim, _ := strconv.Atoi(qv.Get("limit"))
		if lim <= 0 || lim > 200 {
			lim = 40
		}
		si, es := a.kbSearcher()
		if id := qv.Get("id"); id != "" {
			for _, e := range es {
				if e.ID == id {
					writeJSON(w, map[string]interface{}{"item": kbItem(e, true, 0)})
					return
				}
			}
			writeJSON(w, map[string]interface{}{"item": nil})
			return
		}
		ok := func(e kbEntry) bool {
			return (cat == "" || e.Cat == cat) && (typ == "" || e.Type == typ) && (lvl == "" || e.Level == lvl)
		}
		var res []map[string]interface{}
		if q == "" {
			for _, e := range es {
				if ok(e) {
					res = append(res, kbItem(e, false, 0))
				}
			}
		} else {
			for _, h := range si.search(q, 150) {
				if e := es[si.data.Chunks[h.Chunk].File]; ok(e) {
					res = append(res, kbItem(e, false, h.Score))
				}
			}
			sort.SliceStable(res, func(i, j int) bool { return res[i]["score"].(float64) > res[j]["score"].(float64) })
		}
		total := len(res)
		if off > len(res) {
			off = len(res)
		}
		res = res[off:]
		if len(res) > lim {
			res = res[:lim]
		}
		counts, types := map[string]int{}, map[string]int{}
		for _, e := range es {
			counts[e.Cat]++
			types[e.Type]++
		}
		if res == nil {
			res = []map[string]interface{}{}
		}
		writeJSON(w, map[string]interface{}{"items": res, "cats": counts, "types": types, "total": len(es), "matched": total})
	}))
	// learning path: ordered modules of lessons (everything except the glossary), grouped by category
	mux.HandleFunc("/api/hse/kb/path", guard(func(w http.ResponseWriter, r *http.Request) {
		_, es := a.kbSearcher()
		by := map[string][]map[string]interface{}{}
		for _, e := range es {
			if e.Custom || e.Cat == "glossary" || strings.HasPrefix(e.ID, "SG-") { // single-sign cards live in the Signs gallery, not the lesson path
				continue
			}
			by[e.Cat] = append(by[e.Cat], map[string]interface{}{"id": e.ID, "title": e.Title, "type": e.Type, "level": e.Level, "quiz": e.Quiz != nil})
		}
		var mods []map[string]interface{}
		seen := map[string]bool{}
		for _, c := range kbPathOrder {
			if l := by[c]; len(l) > 0 {
				mods = append(mods, map[string]interface{}{"cat": c, "lessons": l})
				seen[c] = true
			}
		}
		for c, l := range by {
			if !seen[c] {
				mods = append(mods, map[string]interface{}{"cat": c, "lessons": l})
			}
		}
		writeJSON(w, map[string]interface{}{"modules": mods})
	}))
}
