package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Translation of user-entered text into the language of the report (ar | fr | en | zh), whatever the input language.
// Pipeline per segment: translation memory (user corrections first) -> exact HSE term -> same-language skip ->
// model with HSE terminology hints + few-shot examples + low temperature -> validation (script, numbers, length) -> retry -> optional review pass.

var trLangs = map[string]string{"ar": "Modern Standard Arabic (العربية الفصحى)", "fr": "French", "en": "English", "zh": "Simplified Chinese (简体中文)"}

func (a *app) reportLang() string {
	if l := a.setting("reportLang"); trLangs[l] != "" {
		return l
	}
	return "fr"
}

func hasLetters(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

type tmEntry struct {
	T string `json:"t"`
	U bool   `json:"u,omitempty"` // corrected by the user: always wins
	N int64  `json:"n"`           // last use (for eviction)
}

var (
	trMu   sync.Mutex
	trMem  = map[string]tmEntry{}
	trInit bool
)

func (a *app) trMemPath() string {
	return filepath.Join(filepath.Dir(filepath.Dir(a.hub.path)), "data", "trmem.json")
}

func (a *app) trLoad() {
	trMu.Lock()
	defer trMu.Unlock()
	if trInit {
		return
	}
	trInit = true
	if b, err := os.ReadFile(a.trMemPath()); err == nil {
		_ = json.Unmarshal(b, &trMem)
	}
	// old format (plain strings) is imported once
	old := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(a.trMemPath()), "trcache.json")); err == nil && json.Unmarshal(b, &old) == nil {
		for k, v := range old {
			if _, ok := trMem[k]; !ok {
				trMem[k] = tmEntry{T: v, N: 1}
			}
		}
	}
}

func (a *app) trSave() {
	trMu.Lock()
	defer trMu.Unlock()
	if len(trMem) > 30000 { // evict the least recently used non-user entries
		type kv struct {
			k string
			n int64
		}
		var l []kv
		for k, e := range trMem {
			if !e.U {
				l = append(l, kv{k, e.N})
			}
		}
		sort.Slice(l, func(i, j int) bool { return l[i].n < l[j].n })
		for i := 0; i < len(l)/3; i++ {
			delete(trMem, l[i].k)
		}
	}
	b, _ := json.Marshal(trMem)
	_ = os.WriteFile(a.trMemPath(), b, 0o644)
}

func tmKey(lang, s string) string { return lang + "|" + strings.Join(strings.Fields(s), " ") }

// ---- language detection
var frWords = strings.Fields("le la les des du de et un une est sont pour dans avec sur pas ne au aux que qui en ce cette ont été a été mais ou par plus sans sous après avant chez nous vous ils elles")
var enWords = strings.Fields("the and is are to of in for with on not no at by from was were be has have it this that an as or but after before without under during all some were been")

func detectLang(s string) string {
	var ar, zh, lat int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Arabic, r):
			ar++
		case unicode.Is(unicode.Han, r):
			zh++
		case unicode.IsLetter(r):
			lat++
		}
	}
	tot := ar + zh + lat
	if tot == 0 {
		return ""
	}
	if zh*2 >= tot {
		return "zh"
	}
	if ar*2 >= tot {
		return "ar"
	}
	toks := strings.Fields(strings.ToLower(regexp.MustCompile(`[^\p{L}' ]+`).ReplaceAllString(s, " ")))
	fr, en := 0, 0
	set := func(w []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range w {
			m[x] = true
		}
		return m
	}
	fs, es := set(frWords), set(enWords)
	for _, t := range toks {
		if fs[t] {
			fr++
		}
		if es[t] {
			en++
		}
	}
	if strings.ContainsAny(strings.ToLower(s), "éèêàùçôîâœ") {
		fr += 2
	}
	switch {
	case fr > en:
		return "fr"
	case en > fr:
		return "en"
	}
	return "" // unknown / too short: let the model decide
}

// ---- terminology from the built-in HSE glossary (ar / fr / en)
type trTerm struct{ L map[string]string }

var (
	termOnce sync.Once
	terms    []trTerm
)

var parenRe = regexp.MustCompile(`\s*\([^)]*\)`)

func termTable() []trTerm {
	termOnce.Do(func() {
		for _, e := range kbBuiltin {
			if e.Cat != "glossary" {
				continue
			}
			t := trTerm{L: map[string]string{}}
			for _, l := range []string{"ar", "fr", "en"} {
				t.L[l] = strings.TrimSpace(e.Title[l])
			}
			terms = append(terms, t)
		}
	})
	return terms
}

func bare(s string) string { return strings.TrimSpace(parenRe.ReplaceAllString(s, "")) }

// termsIn: glossary entries whose name (in any language) appears in the text, longest first
func termsIn(text, to string) []string {
	nt := " " + normQ(text) + " "
	type hit struct {
		src, dst string
		n        int
	}
	var hs []hit
	for _, t := range termTable() {
		dst := bare(t.L[to])
		if dst == "" {
			continue
		}
		for _, l := range []string{"ar", "fr", "en"} {
			if l == to {
				continue
			}
			b := normQ(bare(t.L[l]))
			if len([]rune(b)) < 3 {
				continue
			}
			if strings.Contains(nt, " "+b+" ") || strings.Contains(nt, " "+b+"s ") {
				hs = append(hs, hit{bare(t.L[l]), dst, len(b)})
				break
			}
		}
		// abbreviation in parentheses: "(LOTO)"
		if m := regexp.MustCompile(`\(([A-Z0-9/ ]{2,8})\)`).FindStringSubmatch(t.L["en"]); m != nil && strings.Contains(text, m[1]) {
			hs = append(hs, hit{m[1], bare(t.L[to]) + " (" + m[1] + ")", 2})
		}
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].n > hs[j].n })
	var out []string
	seen := map[string]bool{}
	for _, h := range hs {
		if !seen[h.src] && len(out) < 8 {
			seen[h.src] = true
			out = append(out, h.src+" = "+h.dst)
		}
	}
	return out
}

// exact glossary term (whole segment)
func termExact(seg, to string) (string, bool) {
	ns := normQ(bare(seg))
	if ns == "" {
		return "", false
	}
	for _, t := range termTable() {
		for _, l := range []string{"ar", "fr", "en"} {
			if normQ(bare(t.L[l])) == ns && t.L[to] != "" {
				return bare(t.L[to]), true
			}
		}
	}
	return "", false
}

// ---- few-shot examples (teach the small model the register and what must stay untouched)
var trShots = map[string][][2]string{
	"en": {{"العامل يعمل على المرتفع بدون حزام أمان في أرضية الحفر", "A worker was working at height without a safety harness on the rig floor."}, {"Les extincteurs du camp ont été inspectés ; deux sont à recharger.", "The camp fire extinguishers were inspected; two need to be recharged."}, {"the helment of roustabout is damaged", "The roustabout's helmet is damaged."}},
	"fr": {{"العامل يعمل على المرتفع بدون حزام أمان في أرضية الحفر", "Un ouvrier travaillait en hauteur sans harnais de sécurité sur le plancher de forage."}, {"Housekeeping is poor around the mud pits; tools were left on the walkway.", "Le rangement est insuffisant autour des bacs à boue ; des outils ont été laissés sur la passerelle."}, {"PTW closed after job completion and area cleaned.", "Le PTW a été clôturé après la fin des travaux et la zone a été nettoyée."}},
	"ar": {{"A worker was working at height without a safety harness on the rig floor.", "كان أحد العمال يعمل على ارتفاع دون حزام أمان في أرضية الحفر."}, {"Les extincteurs du camp ont été inspectés ; deux sont à recharger.", "تم تفتيش طفايات الحريق في المخيم، وطفايتان تحتاجان إلى إعادة تعبئة."}, {"Toolbox talk on H2S awareness was held for the night crew.", "أُقيم اجتماع سلامة (Toolbox Talk) حول التوعية بغاز H2S لطاقم الليل."}},
	"zh": {{"A worker was working at height without a safety harness on the rig floor.", "一名工人在钻台上高处作业时未系安全带。"}, {"Les extincteurs du camp ont été inspectés ; deux sont à recharger.", "营地灭火器已检查，其中两个需要重新充装。"}},
}

func trSystem(to string, hints []string) string {
	s := "You are a professional translator of occupational health & safety (HSE) text from an onshore oil drilling rig in Algeria. " +
		"Translate the user's text into " + trLangs[to] + ". The source may be Arabic, French, English or Chinese, often written quickly with spelling mistakes: understand the intended meaning and fix those mistakes silently. " +
		"Rules: keep every number, date, time, name, unit, equipment tag and abbreviation (BOP, PTW, JSA, STOP, PPE, H2S, LTI, POB, SDS, LOTO...) exactly as written; use standard drilling and HSE terminology; do not add, remove, explain or comment; keep the same tone and sentence structure; if the text is already in " + trLangs[to] + ", only correct spelling and grammar. " +
		"Answer with JSON {\"text\": \"<translation>\"} only."
	if len(hints) > 0 {
		s += " Required terminology: " + strings.Join(hints, " ; ") + "."
	}
	return s
}

func digitsOf(s string) string {
	r := strings.NewReplacer("٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4", "٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9", "۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9")
	s = r.Replace(s)
	var d []string
	cur := ""
	for _, c := range s {
		if c >= '0' && c <= '9' {
			cur += string(c)
		} else if cur != "" {
			d = append(d, cur)
			cur = ""
		}
	}
	if cur != "" {
		d = append(d, cur)
	}
	sort.Strings(d)
	return strings.Join(d, ",")
}

var latinAbbr = regexp.MustCompile(`\b[A-Za-z0-9/\-]{2,}\b`)

// valid: is `out` plausibly a faithful translation of `src` into `to`?
func trValid(src, out, to string) bool {
	out = strings.TrimSpace(out)
	if out == "" || len(out) > 4*len(src)+80 {
		return false
	}
	if digitsOf(src) != digitsOf(out) {
		return false
	}
	var ar, zh, lat int
	stripped := latinAbbr.ReplaceAllStringFunc(out, func(w string) string {
		if w == strings.ToUpper(w) || len(w) <= 3 {
			return ""
		}
		return w
	})
	for _, r := range stripped {
		switch {
		case unicode.Is(unicode.Arabic, r):
			ar++
		case unicode.Is(unicode.Han, r):
			zh++
		case unicode.IsLetter(r):
			lat++
		}
	}
	switch to {
	case "ar":
		return ar > 0 && ar*2 >= lat+zh
	case "zh":
		return zh > 0 && zh*2 >= lat
	default:
		return ar == 0 && zh == 0
	}
}

func cleanTr(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"Translation:", "Traduction :", "Traduction:", "الترجمة:", "翻译："} {
		s = strings.TrimSpace(strings.TrimPrefix(s, p))
	}
	return strings.Trim(s, "\"“”")
}

// split a text into segments (lines, then sentences) keeping the separators for reassembly
func trSegments(s string) (segs []string, seps []string) {
	for _, line := range strings.Split(s, "\n") {
		_ = line
	}
	rs := []rune(s)
	start := 0
	flush := func(end, sepEnd int) {
		seg := string(rs[start:end])
		sep := string(rs[end:sepEnd])
		segs = append(segs, seg)
		seps = append(seps, sep)
		start = sepEnd
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\n' {
			j := i
			for j < len(rs) && rs[j] == '\n' {
				j++
			}
			flush(i, j)
			i = j - 1
		} else if (r == '.' || r == '!' || r == '?' || r == '؟' || r == '。' || r == ';') && i+1 < len(rs) && (rs[i+1] == ' ' || r == '。') {
			j := i + 1
			for j < len(rs) && rs[j] == ' ' {
				j++
			}
			flush(i+1, j)
			i = j - 1
		}
	}
	if start < len(rs) {
		segs = append(segs, string(rs[start:]))
		seps = append(seps, "")
	}
	return
}

type trResult struct {
	Text string `json:"text"`
	How  string `json:"how"` // tm | term | same | model | refined | fail
}

// translateSeg: one segment through the whole pipeline
func (a *app) translateSeg(seg, to string, high bool) trResult {
	if !hasLetters(seg) {
		return trResult{seg, "same"}
	}
	key := tmKey(to, seg)
	trMu.Lock()
	if e, ok := trMem[key]; ok {
		e.N = time.Now().Unix()
		trMem[key] = e
		trMu.Unlock()
		return trResult{e.T, "tm"}
	}
	trMu.Unlock()
	if t, ok := termExact(seg, to); ok {
		return trResult{t, "term"}
	}
	if d := detectLang(seg); d == to {
		// already in the report language: only a light spelling pass when the model is up (skipped here for speed)
		return trResult{seg, "same"}
	}
	if err := a.ensureModel(); err != nil {
		return trResult{seg, "fail"}
	}
	hints := termsIn(seg, to)
	msgs := []map[string]string{{"role": "system", "content": trSystem(to, hints)}}
	for _, ex := range trShots[to] {
		j, _ := json.Marshal(map[string]string{"text": ex[1]})
		msgs = append(msgs, map[string]string{"role": "user", "content": ex[0]}, map[string]string{"role": "assistant", "content": string(j)})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": seg})
	maxTok := 3*len([]rune(seg)) + 80
	if maxTok > 700 {
		maxTok = 700
	}
	sch := obj(map[string]interface{}{"text": strT}, "text")
	var out string
	for try, temp := range []float64{0.1, 0.4} {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		res, err := a.llmJSONx(ctx, msgs, sch, maxTok, temp)
		cancel()
		var o struct{ Text string }
		if err != nil || json.Unmarshal([]byte(extractJSONObject(res)), &o) != nil {
			continue
		}
		c := cleanTr(o.Text)
		if trValid(seg, c, to) {
			out = c
			break
		}
		if try == 0 {
			msgs[len(msgs)-1]["content"] = seg + "\n\n(Output ONLY the translation into " + trLangs[to] + "; keep all numbers.)"
		}
	}
	if out == "" {
		return trResult{seg, "fail"}
	}
	how := "model"
	if high { // second pass: a bilingual editor checks the draft against the source
		m2 := []map[string]string{{"role": "system", "content": "You are a bilingual HSE editor. Compare the SOURCE with the DRAFT translation into " + trLangs[to] + ". Fix mistranslations, wrong terminology, missing or added facts and grammar. Keep numbers, names and abbreviations. " + func() string {
			if len(hints) > 0 {
				return "Required terminology: " + strings.Join(hints, " ; ") + ". "
			}
			return ""
		}() + "Answer with JSON {\"text\": \"<final translation>\"} only."}, {"role": "user", "content": "SOURCE:\n" + seg + "\n\nDRAFT:\n" + out}}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		res, err := a.llmJSONx(ctx, m2, sch, maxTok, 0.1)
		cancel()
		var o struct{ Text string }
		if err == nil && json.Unmarshal([]byte(extractJSONObject(res)), &o) == nil {
			if c := cleanTr(o.Text); trValid(seg, c, to) {
				if c != out {
					how = "refined"
				}
				out = c
			}
		}
	}
	trMu.Lock()
	trMem[key] = tmEntry{T: out, N: time.Now().Unix()}
	trMu.Unlock()
	return trResult{out, how}
}

// translateDetailed: whole texts (any length) -> per-text result with how each part was obtained
func (a *app) translateDetailed(texts []string, to string, high bool) ([]trResult, bool) {
	a.trLoad()
	if trLangs[to] == "" {
		to = "en"
	}
	res := make([]trResult, len(texts))
	allOK := true
	for i, t := range texts {
		if !hasLetters(t) {
			res[i] = trResult{t, "same"}
			continue
		}
		segs, seps := trSegments(t)
		var b strings.Builder
		how := "same"
		for j, s := range segs {
			r := a.translateSeg(s, to, high)
			b.WriteString(r.Text + seps[j])
			switch {
			case r.How == "fail":
				how = "fail"
			case how != "fail" && r.How != "same" && (how == "same" || r.How == "model" || r.How == "refined"):
				how = r.How
			}
		}
		res[i] = trResult{strings.TrimSpace(b.String()), how}
		if how == "fail" {
			allOK = false
		}
	}
	a.trSave()
	return res, allOK
}

// translateTexts keeps the old signature used by the report builders (ok=false: the model was needed but unavailable / failed)
func (a *app) translateTexts(texts []string, lang string) ([]string, bool) {
	res, ok := a.translateDetailed(texts, lang, a.setting("trQuality") == "high")
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Text
	}
	return out, ok
}

var _ = fmt.Sprintf

// trRec translates the free-text fields of a record (copy returned).
var trKeys = []string{"text", "desc", "topic", "title", "situation", "scenario", "positives", "objectives", "notes", "improvements", "findings", "conclusion", "activities", "brief", "remarks", "comment"}

func (a *app) trRec(r Rec, lang string) (Rec, bool) {
	c := Rec{}
	for k, v := range r {
		c[k] = v
	}
	var keys, vals []string
	for _, k := range trKeys {
		if s := r.S(k); strings.TrimSpace(s) != "" {
			keys = append(keys, k)
			vals = append(vals, s)
		}
	}
	res, ok := a.translateTexts(vals, lang)
	for i, k := range keys {
		c[k] = res[i]
	}
	return c, ok
}
