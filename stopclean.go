package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ---- STOP database: every card that enters (report upload, archive scan, re-check) is cleaned first:
// text (spacing, encoding, caps, common typos), classification (keyword rules beat a wrong label), then stored with what changed.

type stopChange struct {
	Field string `json:"field"` // text | class | nature | risk
	From  string `json:"from"`
	To    string `json:"to"`
	Why   string `json:"why,omitempty"`
}

var mojibake = strings.NewReplacer("Ã©", "é", "Ã¨", "è", "Ã ", "à", "Ã¢", "â", "Ãª", "ê", "Ã®", "î", "Ã´", "ô", "Ã»", "û", "Ã§", "ç", "Ã‰", "É", "â€™", "’", "â€“", "–", "â€œ", "“", "â€\u009d", "”", "Â ", " ", "Â°", "°", " ", " ", "​", "")

// common misspellings seen on rig cards (English / French)
var typoDict = map[string]string{
	"helment": "helmet", "helemt": "helmet", "helmat": "helmet", "hellmet": "helmet", "glove's": "gloves", "gluves": "gloves", "googles": "goggles", "gogles": "goggles",
	"harnes": "harness", "harnass": "harness", "hamess": "harness", "extinguiser": "extinguisher", "extingisher": "extinguisher", "extinguisher's": "extinguishers", "fire extenguisher": "fire extinguisher",
	"scafolding": "scaffolding", "scaffolfing": "scaffolding", "scafold": "scaffold", "equipement": "equipment", "equiptment": "equipment", "equipments": "equipment", "eqipment": "equipment",
	"proceedure": "procedure", "procedur": "procedure", "proceeding": "procedure", "wearning": "wearing", "wering": "wearing", "wearin": "wearing", "withot": "without", "witout": "without", "whitout": "without",
	"hazzard": "hazard", "hazerd": "hazard", "dangerus": "dangerous", "dangeruos": "dangerous", "dangrous": "dangerous", "unsafty": "unsafe", "unsave": "unsafe", "unsaf": "unsafe", "safty": "safety", "saftey": "safety", "secuirty": "security",
	"enviroment": "environment", "enviornment": "environment", "environnment": "environment", "housekeeping's": "housekeeping", "housekeepin": "housekeeping", "house keeping": "housekeeping", "hose keeping": "housekeeping",
	"pipes's": "pipes", "toolls": "tools", "tooll": "tool", "cabel": "cable", "cabal": "cable", "cabels": "cables", "ladders's": "ladders", "laddder": "ladder", "ladderr": "ladder",
	"oparator": "operator", "opeartor": "operator", "oprator": "operator", "supervisior": "supervisor", "superviser": "supervisor", "responsable": "responsible", "responsabel": "responsible",
	"inspecton": "inspection", "inspction": "inspection", "obsevation": "observation", "observaton": "observation", "obervation": "observation", "observetion": "observation",
	"cleanning": "cleaning", "clening": "cleaning", "stoked": "stocked", "stroage": "storage", "storge": "storage", "stacked's": "stacked", "slippry": "slippery", "slipery": "slippery",
	"extenguisher": "extinguisher", "emergancy": "emergency", "emergancy's": "emergency", "emergncy": "emergency", "evacution": "evacuation", "evacuaton": "evacuation",
	"permite": "permit", "permet": "permit", "pemit": "permit", "chemcial": "chemical", "chemial": "chemical", "dispoal": "disposal", "dispsal": "disposal", "contaner": "container", "contanier": "container",
	"vehical": "vehicle", "vehicule": "vehicle", "vehicales": "vehicles", "seatbelts": "seat belts", "seat belt's": "seat belts", "forklift's": "forklift", "genrator": "generator", "genarator": "generator",
	"corectly": "correctly", "correclty": "correctly", "properley": "properly", "propely": "properly", "properlly": "properly", "wich": "which", "teh": "the", "adn": "and", "recieve": "receive", "occured": "occurred",
	"tounge": "tongue", "tongues": "tongs", "mannual": "manual", "manuel handling": "manual handling", "mud pit's": "mud pits", "rig flor": "rig floor", "rig-floor": "rig floor", "rigfloor": "rig floor",
	"smokng": "smoking", "smokking": "smoking", "ppe's": "PPE", "epi's": "EPI", "h2s's": "H2S", "bop's": "BOP",
}

var wordRe = regexp.MustCompile(`[A-Za-zÀ-ÿ][A-Za-zÀ-ÿ'’\-]*`)

func dedupWords(s string) string {
	f := strings.Fields(s)
	var out []string
	for i, w := range f {
		if i > 0 && len([]rune(w)) >= 2 && strings.EqualFold(strings.Trim(w, ".,;:"), strings.Trim(f[i-1], ".,;:")) && wordRe.MatchString(w) && !strings.ContainsAny(w, "0123456789") {
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}
func collapsePunct(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if strings.ContainsRune(".!?,;:", r) {
			j := i
			for j+1 < len(rs) && rs[j+1] == r {
				j++
			}
			if r == '.' && j-i+1 == 3 {
				b.WriteString("...")
			} else {
				b.WriteRune(r)
			}
			i = j
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var spBeforePunct = regexp.MustCompile(`\s+([,;:.!?])`)
var noSpAfterPunct = regexp.MustCompile(`([,;:!?])([A-Za-zÀ-ÿ])`)

func countRunes(s string, f func(rune) bool) int {
	n := 0
	for _, r := range s {
		if f(r) {
			n++
		}
	}
	return n
}

func mostlyUpper(s string) bool {
	up, lo := countRunes(s, unicode.IsUpper), countRunes(s, unicode.IsLower)
	return up+lo >= 14 && lo*5 <= up
}

func sentenceCase(s string) string {
	l := strings.ToLower(s)
	rs := []rune(l)
	up := true
	for i, r := range rs {
		if up && unicode.IsLetter(r) {
			rs[i] = unicode.ToUpper(r)
			up = false
		}
		if r == '.' || r == '!' || r == '?' {
			up = true
		}
	}
	out := string(rs)
	for _, k := range []string{"PPE", "EPI", "H2S", "BOP", "HSE", "STOP", "PTW", "JSA", "SDS", "LOTO", "POB", "TP", "DP", "CO2", "LEL"} {
		out = regexp.MustCompile(`(?i)\b`+k+`\b`).ReplaceAllString(out, k)
	}
	return out
}

// stopVocab: word frequencies from the cards already stored (verified ones count more) + the knowledge base titles
func (h *hubStore) stopVocab() map[string]int {
	v := map[string]int{}
	h.mu.Lock()
	for _, c := range h.h.Stops {
		w := 1
		if c.Verified {
			w = 3
		}
		for _, m := range wordRe.FindAllString(strings.ToLower(c.Desc+" "+c.Action), -1) {
			v[m] += w
		}
	}
	h.mu.Unlock()
	for _, e := range kbBuiltin {
		for _, l := range []string{"en", "fr"} {
			for _, m := range wordRe.FindAllString(strings.ToLower(e.Title[l]+" "+e.Body[l]), -1) {
				v[m] += 2
			}
		}
	}
	return v
}

func lev(a, b string, max int) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > max || d < -max {
		return max + 1
	}
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		mn := cur[0]
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
			if cur[j] < mn {
				mn = cur[j]
			}
		}
		if mn > max {
			return max + 1
		}
		prev = cur
	}
	return prev[len(rb)]
}

func editDist1(a, b string) bool { // Damerau distance <= 1
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > 1 || d < -1 {
		return false
	}
	if len(ra) == len(rb) {
		diff := []int{}
		for i := range ra {
			if ra[i] != rb[i] {
				diff = append(diff, i)
				if len(diff) > 2 {
					return false
				}
			}
		}
		if len(diff) <= 1 {
			return len(diff) == 1
		}
		return diff[1] == diff[0]+1 && ra[diff[0]] == rb[diff[1]] && ra[diff[1]] == rb[diff[0]]
	}
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	i := 0
	for i < len(rb) && ra[i] == rb[i] {
		i++
	}
	return string(ra[i+1:]) == string(rb[i:])
}

// fixText: returns the cleaned text and the list of word replacements (for the review table)
func fixText(s string, vocab map[string]int) (string, []string) {
	var notes []string
	o := s
	s = mojibake.Replace(s)
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	s = normSpaces(s)
	s = collapsePunct(s)
	s = spBeforePunct.ReplaceAllString(s, "$1")
	s = noSpAfterPunct.ReplaceAllString(s, "$1 $2")
	if mostlyUpper(s) {
		s = sentenceCase(s)
		notes = append(notes, "CAPS→Aa")
	}
	s = dedupWords(s)
	// typo dictionary (phrases first) then fuzzy match against the vocabulary
	low := strings.ToLower(s)
	for k, v := range typoDict {
		if strings.Contains(k, " ") && strings.Contains(low, k) {
			s = regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(k)+`\b`).ReplaceAllString(s, v)
			notes = append(notes, k+"→"+v)
			low = strings.ToLower(s)
		}
	}
	s = wordRe.ReplaceAllStringFunc(s, func(w string) string {
		lw := strings.ToLower(w)
		if v, ok := typoDict[lw]; ok && !strings.Contains(lw, " ") {
			notes = append(notes, w+"→"+v)
			return matchCase(w, v)
		}
		return w
	})
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 0 && unicode.IsLower(r[0]) {
		r[0] = unicode.ToUpper(r[0])
		s = string(r)
	}
	if o == s {
		return s, nil
	}
	return s, notes
}

func capFirst(s string) string {
	if r := []rune(s); len(r) > 0 && unicode.IsLower(r[0]) {
		r[0] = unicode.ToUpper(r[0])
		return string(r)
	}
	return s
}

func onlyCapFirst(a, b string) bool { return capFirst(strings.TrimSpace(a)) == b }

func matchCase(from, to string) string {
	if from == strings.ToUpper(from) && len(from) > 1 {
		return strings.ToUpper(to)
	}
	if r := []rune(from); unicode.IsUpper(r[0]) {
		t := []rune(to)
		t[0] = unicode.ToUpper(t[0])
		return string(t)
	}
	return to
}

// ---- classification rules (EN / FR / AR keywords). Strong evidence beats a wrong label.
var classRules = []struct {
	cls string
	kw  []string
}{
	{"EPI", []string{"helmet", "hard hat", "hardhat", "casque", "خوذ", "glove", "gant", "قفاز", "goggle", "glasses", "lunette", "نظار", "safety shoe", "safety boot", "boots", "chaussure", "حذاء", "ear plug", "earplug", "ear muff", "bouchon", "ppe", "epi ", "coverall", "combinaison", "vest", "gilet", "uniform", "mask", "masque", "respirator", "harness", "harnais", "حزام الأمان", "face shield", "visiere", "visière"}},
	{"Position dangereuse", []string{"line of fire", "ligne de tir", "pinch", "slip", "trip hazard", "glissad", "chute", "falling", "fall from", "at height", "working at height", "hauteur", "ladder", "echelle", "échelle", "hands between", "under load", "sous charge", "under suspended", "bending", "posture", "manual handling", "lifting by hand", "standing on", "walking on", "climbing", "سقوط", "ارتفاع", "وضعية"}},
	{"Outils & matériels", []string{"tool", "outil", "equipment", "équipement", "equipement", "machine", "crane", "grue", "hose", "flexible", "cable", "câble", "damaged", "défectueux", "defect", "broken", "cassé", "faulty", "unguarded", "guard", "calibration", "expired", "welding", "generator", "extinguisher", "extincteur", "forklift", "vehicle", "véhicule", "sling", "shackle", "chain", "wrench", "clé", "hammer", "grinder", "meuleuse", "عدة", "معدات", "أداة", "رافعة"}},
	{"Procédures & Ordre", []string{"housekeeping", "clean", "tidy", "untidy", "order", "rangement", "storage", "stack", "waste", "garbage", "trash", "permit", "ptw", "jsa", "procedure", "procédure", "toolbox", "lockout", "loto", "signage", "sign ", "barrier", "barricade", "tape", "permis", "unauthori", "speed", "seat belt", "seatbelt", "parking", "park", "smoking", "fumer", "ترتيب", "نظافة", "إجراء", "تصريح"}},
	{"Réaction des Personnes", []string{"reaction", "réaction", "changed position", "stopped work", "asked", "aware", "distract", "rushing", "hurry", "fatigue", "tired", "complacen", "phone", "mobile", "texting", "joking", "horseplay", "ردة فعل", "تشتت", "تعب", "هاتف"}},
}

func ruleClass(text string) (cls string, hits []string, score map[string]int) {
	t := " " + strings.ToLower(frAccents.Replace(text)) + " "
	t2 := " " + strings.ToLower(text) + " "
	score = map[string]int{}
	for _, r := range classRules {
		for _, k := range r.kw {
			if strings.Contains(t, " "+strings.TrimSpace(k)) || strings.Contains(t2, strings.TrimSpace(k)) && len(k) > 4 {
				score[r.cls]++
				if r.cls != "" {
					hits = append(hits, k)
				}
			}
		}
	}
	if envRe.MatchString(text) {
		score["Environnement"] += 3
	}
	best, bs, tie := "", 0, false
	for c, s := range score {
		if s > bs {
			best, bs, tie = c, s, false
		} else if s == bs {
			tie = true
		}
	}
	if tie || bs == 0 {
		return "", nil, score
	}
	return best, hits, score
}

var posRe = regexp.MustCompile(`(?i)(good practice|well done|bonne pratique|good job|correctly|properly wearing|wearing .* properly|respect(s|ed|ing)?|compliant|conforme|excellent|حسن|ممارسة جيدة|التزام)`)
var negRe = regexp.MustCompile(`(?i)(\bno\b|\bnot\b|without|missing|\bsans\b|absence|damaged|leak|broken|unsafe|dirty|untidy|obstruct|defect|expired|بدون|مفقود|تسرب|مكسور|غير)`)
var condRe = regexp.MustCompile(`(?i)(leak|damaged|broken|missing|defect|spill|unguarded|no sign|dirty|untidy|obstruct|expired|slippery|exposed|fuite|cassé|défectueux|تسرب|مكسور|مفقود)`)

// cleanCard: normalises + fixes one card in place; returns the changes made
func (h *hubStore) cleanCard(c *StopCard, vocab map[string]int, clf *stopClassifier) []stopChange {
	var ch []stopChange
	od, oa, oc, on := c.Desc, c.Action, c.Class, c.Nature
	normStop(c)
	if d, notes := fixText(c.Desc, vocab); d != c.Desc && !onlyCapFirst(c.Desc, d) {
		ch = append(ch, stopChange{"text", c.Desc, d, strings.Join(notes, ", ")})
		c.Desc = d
	}
	c.Desc = capFirst(c.Desc)
	if a, notes := fixText(c.Action, vocab); a != c.Action && !onlyCapFirst(c.Action, a) {
		ch = append(ch, stopChange{"text", c.Action, a, strings.Join(notes, ", ")})
		c.Action = a
	}
	text := c.Desc + " " + c.Action
	// nature
	if posRe.MatchString(text) && !negRe.MatchString(c.Desc) && c.Nature != "Positive observation" && c.Nature != "" {
		ch = append(ch, stopChange{"nature", c.Nature, "Positive observation", "good-practice wording"})
		c.Nature = "Positive observation"
	} else if c.Nature == "Positive observation" && negRe.MatchString(c.Desc) && !posRe.MatchString(text) {
		to := "Unsafe Act"
		if condRe.MatchString(c.Desc) {
			to = "Unsafe Condition"
		}
		ch = append(ch, stopChange{"nature", c.Nature, to, "negative wording"})
		c.Nature = to
	} else if c.Nature == "" {
		n, _, _, cf := clf.classify(*c)
		if cf >= 0.35 {
			ch = append(ch, stopChange{"nature", "", n, "learned from your cards"})
			c.Nature = n
		}
	}
	if c.Nature == "Positive observation" && strings.TrimSpace(c.Action) == "" {
		c.Action = "Good practice"
	}
	// class
	rc, hits, score := ruleClass(text)
	switch {
	case c.Class == "" || c.Class == "Autres" && rc != "":
		to := rc
		why := "keywords: " + strings.Join(firstN(hits, 3), ", ")
		if to == "" {
			_, cl, _, cf := clf.classify(*c)
			if cf >= 0.35 {
				to, why = cl, "learned from your cards"
			}
		}
		if to != "" && to != c.Class {
			ch = append(ch, stopChange{"class", c.Class, to, why})
			c.Class = to
		}
	case rc != "" && rc != c.Class && score[rc] >= 2 && score[c.Class] == 0:
		ch = append(ch, stopChange{"class", c.Class, rc, "keywords: " + strings.Join(firstN(hits, 3), ", ")})
		c.Class = rc
	}
	if c.Class == "Environnement" && c.Nature != "Positive observation" && c.Nature != "Environment" {
		ch = append(ch, stopChange{"nature", c.Nature, "Environment", "environment class"})
		c.Nature = "Environment"
	}
	if c.Risk == "" && c.Nature != "Positive observation" {
		if _, _, rk, cf := clf.classify(*c); rk != "" && cf >= 0.35 {
			ch = append(ch, stopChange{"risk", "", rk, "learned from your cards"})
			c.Risk = rk
		}
	}
	if len(ch) > 0 {
		seen := map[string]bool{}
		c.Fixed = nil
		for _, x := range ch {
			if !seen[x.Field] {
				seen[x.Field] = true
				c.Fixed = append(c.Fixed, x.Field)
			}
		}
		if len(c.Orig) == 0 {
			c.Orig = []string{od, oa, oc, on}
		}
	}
	return ch
}

// cleanBatch: rule-based cleaning for automatic imports (archive scan)
func (h *hubStore) cleanBatch(cards []StopCard) []StopCard {
	vocab := h.stopVocab()
	for i := range cards {
		h.cleanCard(&cards[i], vocab, h.classifierFor(cards[i].Scheme))
	}
	return cards
}

func stopKey(date, desc string) string {
	return date + "|" + strings.Join(strings.FieldsFunc(strings.ToLower(desc), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

func (a *app) stopDBHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	h := a.hub
	go func() { // warm the classifiers in the background so the first suggestion is instant
		time.Sleep(2 * time.Second)
		if a.setting("stopSeedDone") == "" {
			h.importSeed() // first run: load the cleaned STOP cards shipped with the program (duplicates skipped)
		}
		if a.setting("stopOpsDone") == "" {
			h.importOps()
		}
		for _, sch := range []string{"sonatrach", "sinopec", ""} {
			h.classifierFor(sch)
		}
	}()
	tmpDir := func() string {
		d := filepath.Join(filepath.Dir(h.path), "stoptmp")
		_ = os.MkdirAll(d, 0o755)
		return d
	}
	// 1) upload a previous report -> cleaned cards for review (nothing is stored yet)
	mux.HandleFunc("/api/hse/stopdb/preview", guard(func(w http.ResponseWriter, r *http.Request) {
		name := safeName(filepath.Base(r.URL.Query().Get("name")))
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".xls" && ext != ".xlsx" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "الملف يجب أن يكون .xls أو .xlsx"})
			return
		}
		path := filepath.Join(tmpDir(), name)
		f, err := os.Create(path)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, 80<<20))
		f.Close()
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		cards, format, err := importStopFile(path, r.URL.Query().Get("company"))
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		vocab := h.stopVocab()
		h.mu.Lock()
		have := map[string]bool{}
		for _, c := range h.h.Stops {
			have[stopKey(c.Date, c.Desc)] = true
			if len(c.Orig) > 0 {
				have[stopKey(c.Date, c.Orig[0])] = true
			}
		}
		h.mu.Unlock()
		items := []map[string]interface{}{}
		sum := map[string]int{"read": len(cards), "text": 0, "class": 0, "nature": 0, "risk": 0, "dup": 0}
		for i := range cards {
			cards[i].Src = name
			orig := cards[i]
			ch := h.cleanCard(&cards[i], vocab, h.classifierFor(cards[i].Scheme))
			dup := have[stopKey(orig.Date, orig.Desc)] || have[stopKey(cards[i].Date, cards[i].Desc)]
			for _, x := range ch {
				sum[x.Field]++
			}
			if dup {
				sum["dup"]++
			}
			items = append(items, map[string]interface{}{"card": cards[i], "changes": ch, "dup": dup})
		}
		writeJSON(w, map[string]interface{}{"ok": true, "format": format, "file": name, "items": items, "summary": sum})
	}))
	// 2) store the reviewed cards (marked verified: they teach the classifier) and file the report in STOP_Archive
	mux.HandleFunc("/api/hse/stopdb/commit", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Cards []StopCard `json:"cards"`
			File  string     `json:"file"`
			Auto  bool       `json:"auto"` // automatic import: not reviewed by a person, so not marked verified
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 80<<20)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if in.Auto {
			// automatic import: no person reviewed it, so the user's own labels stay as written (the model only fills
			// empty ones and proposes the rest later through "re-check"); text fixes are kept; one-word notes are dropped.
			keep := in.Cards[:0]
			for _, c := range in.Cards {
				if len(strings.Fields(c.Desc)) < 3 {
					continue
				}
				if len(c.Orig) == 4 {
					if oc := normClass(c.Orig[2]); oc != "" && c.Class != oc {
						c.Class = oc
						c.Fixed = delStr(c.Fixed, "class")
					}
					if on := normNature(c.Orig[3]); on != "" && c.Nature != on {
						c.Nature = on
						c.Fixed = delStr(c.Fixed, "nature")
					}
				}
				keep = append(keep, c)
			}
			in.Cards = keep
		}
		for i := range in.Cards {
			in.Cards[i].Verified = !in.Auto
			if in.Cards[i].Source == "" {
				in.Cards[i].Source = "archive"
			}
			in.Cards[i].ID = ""
		}
		add, dup := h.addStops(in.Cards)
		h.mu.Lock()
		h.ver++
		h.mu.Unlock()
		moved := false
		if fn := safeName(filepath.Base(in.File)); fn != "" {
			src := filepath.Join(tmpDir(), fn)
			if _, err := os.Stat(src); err == nil {
				dst := filepath.Join(a.stopArchDir(), fn)
				if os.Rename(src, dst) != nil {
					if b, e := os.ReadFile(src); e == nil && os.WriteFile(dst, b, 0o644) == nil {
						_ = os.Remove(src)
					}
				}
				if st2, e := os.Stat(dst); e == nil {
					done := map[string]string{}
					_ = json.Unmarshal([]byte(a.setting("stopArchDone")), &done)
					done[fn] = fmt.Sprintf("%d|%d", st2.Size(), st2.ModTime().Unix())
					b, _ := json.Marshal(done)
					h.mu.Lock()
					h.h.Settings["stopArchDone"] = string(b)
					_ = h.saveLocked()
					h.mu.Unlock()
					moved = true
				}
			}
		}
		writeJSON(w, map[string]interface{}{"ok": true, "added": add, "dup": dup, "filed": moved})
	}))
	// 3) re-check what is already stored: proposals only (applied through /api/hub/stop/update after review)
	mux.HandleFunc("/api/hse/stopdb/recheck", guard(func(w http.ResponseWriter, r *http.Request) {
		vocab := h.stopVocab()
		h.mu.Lock()
		cur := append([]StopCard{}, h.h.Stops...)
		h.mu.Unlock()
		items := []map[string]interface{}{}
		total := 0
		for _, c := range cur {
			if c.Verified {
				continue
			}
			c2 := c
			ch := h.cleanCard(&c2, vocab, h.classifierFor(c2.Scheme))
			if len(ch) == 0 {
				continue
			}
			total++
			if len(items) < 400 {
				items = append(items, map[string]interface{}{"card": c2, "changes": ch})
			}
		}
		writeJSON(w, map[string]interface{}{"ok": true, "items": items, "total": total, "checked": len(cur)})
	}))
	mux.HandleFunc("/api/hse/stopdb/stats", guard(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		by := map[string]map[string]int{"class": {}, "nature": {}, "year": {}, "company": {}, "scheme": {}, "origin": {}, "source": {}}
		pn, pok := 0, 0
		ver, fixed, unclass := 0, 0, 0
		for _, c := range h.h.Stops {
			by["class"][firstOf(c.Class, "—")]++
			by["nature"][firstOf(c.Nature, "—")]++
			by["company"][firstOf(c.Company, "—")]++
			by["scheme"][firstOf(c.Scheme, "—")]++
			by["origin"][stopOrigin(c.Source)]++
			by["source"][firstOf(c.Source, "—")]++
			if len(c.Pred) == 3 && c.Confirmed {
				pn++
				if c.Pred[0] == c.Nature && c.Pred[1] == c.Class && c.Pred[2] == c.Risk {
					pok++
				}
			}
			if len(c.Date) >= 4 {
				by["year"][c.Date[:4]]++
			}
			if c.Verified {
				ver++
			}
			if len(c.Fixed) > 0 {
				fixed++
			}
			if c.Class == "" || c.Nature == "" {
				unclass++
			}
		}
		keys := map[string][]string{}
		for k, m := range by {
			for x := range m {
				keys[k] = append(keys[k], x)
			}
			sort.Strings(keys[k])
		}
		writeJSON(w, map[string]interface{}{"total": len(h.h.Stops), "verified": ver, "fixed": fixed, "unclassified": unclass, "by": by, "suggested": pn, "accepted": pok})
	}))
}

// stopOrigin separates cards typed/imported from real records ("manual") from cards produced by the program ("generated").
func stopOrigin(src string) string {
	switch src {
	case "dict", "ai", "generic":
		return "generated"
	}
	return "manual"
}

func delStr(a []string, x string) []string {
	out := a[:0]
	for _, v := range a {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
