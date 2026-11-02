package main

// STOP cards: import (Sonatrach-style log / OSCO-style register), label normalisation,
// a naive-Bayes classifier learned from the user's own cards, and description-based generation.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var stopNatures = []string{"Unsafe Act", "Unsafe Condition", "Positive observation", "Environment"}
var stopClasses = []string{"Réaction des Personnes", "EPI", "Position dangereuse", "Outils & matériels", "Procédures & Ordre", "Environnement", "Autres"}

func lc(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func normNature(s string) string {
	l := lc(s)
	switch {
	case l == "":
		return ""
	case strings.Contains(l, "positive") || strings.Contains(l, "good practice") || strings.Contains(l, "bonne pratique"):
		return "Positive observation"
	case strings.Contains(l, "act"):
		return "Unsafe Act"
	case strings.Contains(l, "cond"):
		return "Unsafe Condition"
	case strings.Contains(l, "environ"):
		return "Environment"
	}
	return "" // not one of the known natures (e.g. a sub-category that leaked into this column): the classifier proposes one
}

func normClass(s string) string {
	l := lc(s)
	switch {
	case l == "":
		return ""
	case l == "ppe" || strings.Contains(l, "epi") || strings.Contains(l, "protective"):
		return "EPI"
	case strings.Contains(l, "réaction") || strings.Contains(l, "reaction"):
		return "Réaction des Personnes"
	case strings.Contains(l, "position") || strings.Contains(l, "falling") || strings.Contains(l, "slip"):
		return "Position dangereuse"
	case strings.Contains(l, "outil") || strings.Contains(l, "tool") || strings.Contains(l, "equip") || strings.Contains(l, "matériel"):
		return "Outils & matériels"
	case strings.Contains(l, "proc") || strings.Contains(l, "housekeeping") || strings.Contains(l, "ordre"):
		return "Procédures & Ordre"
	case strings.Contains(l, "environ"):
		return "Environnement"
	case strings.Contains(l, "autre") || l == "others" || l == "other":
		return "Autres"
	}
	return strings.TrimSpace(s)
}

func normRisk(s string) string {
	switch lc(s) {
	case "high", "haut", "élevé", "eleve":
		return "High"
	case "medium", "moyen", "med":
		return "Medium"
	case "low", "faible", "bas":
		return "Low"
	case "na", "n/a", "":
		return ""
	}
	return strings.TrimSpace(s)
}

func normStatus(s string) string {
	switch {
	case strings.HasPrefix(lc(s), "clos"), strings.HasPrefix(lc(s), "ferm"):
		return "Closed"
	case strings.HasPrefix(lc(s), "open"), strings.HasPrefix(lc(s), "ouv"):
		return "Open"
	}
	return strings.TrimSpace(s)
}

var spaceRe = regexp.MustCompile(`\s+`)

func normZone(s string) string {
	l := spaceRe.ReplaceAllString(lc(s), " ")
	switch l {
	case "well site", "wellsite", "well  site":
		return "Well site"
	case "rig site", "rigsite":
		return "Rig site"
	case "rig floor", "rg floor", "rigfloor", "plancher":
		return "Rig floor"
	}
	if l == "" {
		return ""
	}
	r := []rune(strings.TrimSpace(s))
	if strings.ToUpper(string(r)) == string(r) && len(r) > 4 { // SHOUTING -> Normal
		l = strings.ToLower(string(r))
		rr := []rune(l)
		rr[0] = []rune(strings.ToUpper(string(rr[0])))[0]
		return string(rr)
	}
	return strings.TrimSpace(s)
}

var funcMap = map[string]string{"hse": "HSE", "a/d": "A/D", "a-d": "A/D", "floorman": "Floorman", "floor man": "Floorman", "derrick man": "Derrickman", "derrickman": "Derrickman",
	"roustabout": "Roustabout", "roustbout": "Roustabout", "tp": "Toolpusher", "toolpusher": "Toolpusher", "night toolpusher": "Night Toolpusher", "motor man": "Motorman", "motorman": "Motorman",
	"for man": "Foreman", "foreman": "Foreman", "asst mec": "Asst Mechanic", "asst elec": "Asst Electrician", "elec": "Electrician"}

func normFunc(s string) string {
	l := spaceRe.ReplaceAllString(lc(s), " ")
	if v, ok := funcMap[l]; ok {
		return v
	}
	return strings.TrimSpace(s)
}

var dateRes = []struct {
	re  *regexp.Regexp
	fmt string
}{
	{regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`), "2006-01-02"},
	{regexp.MustCompile(`^\d{1,2}/\d{1,2}/\d{4}`), "02/01/2006"},
	{regexp.MustCompile(`^\d{1,2}-\d{1,2}-\d{4}`), "02-01-2006"},
	{regexp.MustCompile(`^\d{1,2}\.\d{1,2}\.\d{4}`), "02.01.2006"},
}

func normDateAny(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if d := excelSerialDate(s); d != "" {
		return d
	}
	if m := regexp.MustCompile(`^(\d{4})[.\-/](\d{1,2})$`).FindStringSubmatch(s); m != nil { // "2026.09"
		mo, _ := strconv.Atoi(m[2])
		return fmt.Sprintf("%s-%02d-01", m[1], mo)
	}
	for _, d := range dateRes {
		if d.re.MatchString(s) {
			part := d.re.FindString(s)
			if t, err := time.Parse(d.fmt, part); err == nil {
				return t.Format("2006-01-02")
			}
			if strings.Contains(part, "/") {
				if t, err := time.Parse("1/2/2006", part); err == nil {
					return t.Format("2006-01-02")
				}
			}
		}
	}
	return ""
}

func normStop(c *StopCard) {
	c.Desc = normSpaces(c.Desc)
	c.Action = normSpaces(c.Action)
	c.Nature, c.Class, c.Risk, c.Status = normNature(c.Nature), normClass(c.Class), normRisk(c.Risk), normStatus(c.Status)
	c.Zone, c.Function = normZone(c.Zone), normFunc(c.Function)
	c.Observer, c.Company, c.Responsible = normSpaces(c.Observer), normSpaces(c.Company), normSpaces(c.Responsible)
	if c.Nature == "Positive observation" && strings.EqualFold(c.Action, "good practice") {
		c.Action = "Good practice"
	}
}

// ----------------------------------------------------------------------------- import

func hdrKey(s string) string {
	s = strings.ToLower(spaceRe.ReplaceAllString(strings.TrimSpace(s), " "))
	return s
}

// importStopFile reads a STOP-card log in either format and returns the cards.
func importStopFile(path, defCompany string) ([]StopCard, string, error) {
	grids, err := readGrids(path)
	if err != nil {
		return nil, "", err
	}
	var out []StopCard
	format := ""
	for _, g := range grids {
		// find a header row
		for r := 0; r < len(g.Rows) && r < 30; r++ {
			cols := map[string]int{}
			for c, v := range g.Rows[r] {
				cols[hdrKey(v)] = c
			}
			find := func(keys ...string) int {
				for _, k := range keys {
					for h, c := range cols {
						if h != "" && strings.Contains(h, k) {
							return c
						}
					}
				}
				return -1
			}
			// Sonatrach style
			if dc, nc := find("description"), find("nature"); dc >= 0 && nc >= 0 {
				ci := map[string]int{"date": find("date"), "action": find("immediate"), "func": find("fonction", "function"), "zone": find("zone"), "class": find("classification"),
					"risk": find("niveau", "risk"), "dead": find("ech", "deadline"), "resp": find("sponsable", "responsible"), "status": find("etat", "état", "status")}
				for rr := r + 1; rr < len(g.Rows); rr++ {
					desc := gridCell(g.Rows, rr, dc)
					if desc == "" {
						continue
					}
					c := StopCard{Desc: desc, Action: gridCell(g.Rows, rr, ci["action"]), Function: gridCell(g.Rows, rr, ci["func"]), Zone: gridCell(g.Rows, rr, ci["zone"]),
						Nature: gridCell(g.Rows, rr, nc), Class: gridCell(g.Rows, rr, ci["class"]), Risk: gridCell(g.Rows, rr, ci["risk"]), Deadline: gridCell(g.Rows, rr, ci["dead"]),
						Responsible: gridCell(g.Rows, rr, ci["resp"]), Status: gridCell(g.Rows, rr, ci["status"]), Company: defCompany, Source: "log"}
					c.Date = normDateAny(gridCell(g.Rows, rr, ci["date"]))
					if ci["date"] < 0 {
						c.Date = ""
					}
					c.Observer = ""
					normStop(&c)
					out = append(out, c)
				}
				format = "sonatrach"
				break
			}
			// GWDC "General Tracking Register" style (Type of Report / Related to / Reported by)
			if tc, rc, bc := find("type of report"), find("related to"), find("reported by"); tc >= 0 && rc >= 0 && bc >= 0 {
				ci := map[string]int{"date": find("date"), "pos": find("position"), "comp": find("company"), "place": find("place"), "desc": find("description"), "act": find("action taken"), "prio": find("priority"), "status": find("status"), "resp": find("responsible")}
				for rr := r + 1; rr < len(g.Rows); rr++ {
					if !strings.HasPrefix(lc(gridCell(g.Rows, rr, tc)), "stop") {
						continue
					}
					desc := gridCell(g.Rows, rr, ci["desc"])
					if desc == "" {
						continue
					}
					c := StopCard{Desc: desc, Action: gridCell(g.Rows, rr, ci["act"]), Observer: gridCell(g.Rows, rr, bc), Function: gridCell(g.Rows, rr, ci["pos"]),
						Company: firstOf(gridCell(g.Rows, rr, ci["comp"]), defCompany), Zone: gridCell(g.Rows, rr, ci["place"]), Status: gridCell(g.Rows, rr, ci["status"]),
						Responsible: gridCell(g.Rows, rr, ci["resp"]), Source: "gwdc"}
					c.Date = normDateAny(gridCell(g.Rows, rr, ci["date"]))
					rel := gridCell(g.Rows, rr, rc)
					c.Tags = []string{rel}
					c.Class, c.Nature = gwdcClass(rel)
					switch strings.ToUpper(gridCell(g.Rows, rr, ci["prio"])) {
					case "H":
						c.Risk = "High"
					case "M":
						c.Risk = "Medium"
					case "L":
						c.Risk = "Low"
					case "P":
						c.Nature = "Positive observation"
					}
					if la := lc(c.Action); strings.Contains(la, "good action") || strings.Contains(la, "good practice") || strings.Contains(la, "well done") || strings.Contains(la, "positive") {
						c.Nature = "Positive observation"
					}
					if strings.EqualFold(c.Zone, "location") {
						c.Zone = "Rig site"
					}
					normStop(&c)
					out = append(out, c)
				}
				format = "gwdc"
				break
			}
			// OSCO / register style
			if oc, bc := find("observer"), find("observations"); oc >= 0 && bc >= 0 {
				ci := map[string]int{"date": find("date"), "post": find("post"), "comp": find("company"), "risk": find("risk"), "area": find("area"), "act": find("immediate")}
				// sub-category columns after the observation column: positive, reactions, ppe, positions, procedures, tools (data layout of the register)
				groups := []struct {
					col   int
					class string
				}{{10, "Réaction des Personnes"}, {12, "EPI"}, {14, "Position dangereuse"}, {16, "Procédures & Ordre"}, {18, "Outils & matériels"}}
				actCol := ci["act"]
				if actCol < 0 {
					actCol = 20
				}
				for rr := r + 1; rr < len(g.Rows); rr++ {
					desc := gridCell(g.Rows, rr, bc)
					if desc == "" {
						continue
					}
					if n := gridCell(g.Rows, rr, 0); strings.EqualFold(n, "total") {
						continue
					}
					c := StopCard{Desc: desc, Observer: gridCell(g.Rows, rr, oc), Function: gridCell(g.Rows, rr, ci["post"]), Company: gridCell(g.Rows, rr, ci["comp"]),
						Risk: gridCell(g.Rows, rr, ci["risk"]), Zone: gridCell(g.Rows, rr, ci["area"]), Source: "register"}
					if c.Company == "" {
						c.Company = defCompany
					}
					c.Date = normDateAny(gridCell(g.Rows, rr, ci["date"]))
					act := gridCell(g.Rows, rr, actCol)
					c.Action = act
					c.Status = "Closed"
					for k := actCol + 1; k < actCol+5; k++ {
						if v := normStatus(gridCell(g.Rows, rr, k)); v == "Open" || v == "Closed" {
							c.Status = v
							break
						}
					}
					positive := strings.Contains(lc(gridCell(g.Rows, rr, 8)), "positive")
					var tags []string
					classHit := map[string]bool{}
					for _, gp := range groups {
						if v := gridCell(g.Rows, rr, gp.col); v != "" && !isNumStr(v) {
							tags = append(tags, v)
							classHit[gp.class] = true
						}
					}
					c.Tags = tags
					for _, cl := range []string{"EPI", "Position dangereuse", "Outils & matériels", "Procédures & Ordre", "Réaction des Personnes", "Autres"} {
						if classHit[cl] {
							c.Class = cl
							break
						}
					}
					switch {
					case positive:
						c.Nature = "Positive observation"
					case classHit["Réaction des Personnes"]:
						c.Nature = "Unsafe Act"
					case len(tags) > 0:
						c.Nature = "Unsafe Condition"
					}
					if positive && strings.EqualFold(c.Action, "positive observation") {
						c.Action = "Good practice"
					}
					normStop(&c)
					out = append(out, c)
				}
				format = "osco"
				break
			}
		}
		if format != "" {
			break
		}
	}
	if format == "" {
		return nil, "", fmt.Errorf("لم أتعرّف على شكل الملف (أحتاج أعمدة مثل Description/Nature أو Observer/Observations)")
	}
	sch := "sinopec"
	if format == "sonatrach" {
		sch = "sonatrach"
	}
	for i := range out {
		out[i].Scheme = sch
	}
	return out, format, nil
}

// addStops merges cards into the database (skipping duplicates). Returns added / duplicates.
func (h *hubStore) addStops(cards []StopCard) (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	for _, c := range h.h.Stops {
		seen[stopKey(c.Date, c.Desc)] = true
		if len(c.Orig) > 0 {
			seen[stopKey(c.Date, c.Orig[0])] = true
		}
	}
	add, dup := 0, 0
	for _, c := range cards {
		k := stopKey(c.Date, c.Desc)
		if len(c.Orig) > 0 && seen[stopKey(c.Date, c.Orig[0])] {
			dup++
			continue
		}
		if seen[k] {
			dup++
			continue
		}
		seen[k] = true
		if c.ID == "" {
			c.ID = newID()
		}
		h.h.Stops = append(h.h.Stops, c)
		add++
	}
	_ = h.saveLocked()
	return add, dup
}

// ----------------------------------------------------------------------------- classifier

type nbModel struct {
	prior map[string]float64
	cond  map[string]map[string]float64 // label -> token -> count
	tot   map[string]float64
	vocab map[string]bool
	n     int
}

func trainNB(docs [][]string, labels []string) *nbModel {
	m := &nbModel{prior: map[string]float64{}, cond: map[string]map[string]float64{}, tot: map[string]float64{}, vocab: map[string]bool{}}
	for i, d := range docs {
		l := labels[i]
		if l == "" {
			continue
		}
		m.n++
		m.prior[l]++
		if m.cond[l] == nil {
			m.cond[l] = map[string]float64{}
		}
		for _, t := range d {
			m.cond[l][t]++
			m.tot[l]++
			m.vocab[t] = true
		}
	}
	return m
}

func (m *nbModel) predict(toks []string) (string, float64) {
	if m == nil || m.n < 15 {
		return "", 0
	}
	type sc struct {
		l string
		s float64
	}
	var ss []sc
	V := float64(len(m.vocab) + 1)
	for l, p := range m.prior {
		s := math.Log(p / float64(m.n))
		for _, t := range toks {
			s += math.Log((m.cond[l][t] + 0.5) / (m.tot[l] + 0.5*V))
		}
		ss = append(ss, sc{l, s})
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].s > ss[j].s })
	var z float64
	for _, x := range ss {
		z += math.Exp(x.s - ss[0].s)
	}
	return ss[0].l, 1 / z
}

type stopClassifier struct {
	nature, class, risk *linModel
	n                   int
}

func stopText(c StopCard) []string {
	return tokenize(c.Desc + " " + c.Action)
}

// classifierFor returns the model of one company scheme (sonatrach / sinopec; "" = all cards). With fewer than
// 150 cards of that scheme it falls back to the combined model. Training happens outside the hub lock.
func (h *hubStore) classifierFor(scheme string) *stopClassifier {
	h.mu.Lock()
	var docs [][]string
	var ln, lc2, lr []string
	n := 0
	for _, c := range h.h.Stops {
		if scheme == "" || c.Scheme == scheme {
			n++
		}
	}
	if scheme != "" && n < 150 {
		h.mu.Unlock()
		return h.classifierFor("")
	}
	key := n + h.ver*1000003
	if h.clfs != nil && h.clfs[scheme] != nil && h.clfK[scheme] == key {
		m := h.clfs[scheme]
		h.mu.Unlock()
		return m
	}
	for _, c := range h.h.Stops {
		if scheme != "" && c.Scheme != scheme {
			continue
		}
		rep := 1
		if c.Verified {
			rep = 3 // user-corrected cards teach the model more
		}
		for i := 0; i < rep; i++ {
			docs = append(docs, stopText(c))
			ln = append(ln, c.Nature)
			lc2 = append(lc2, c.Class)
			lr = append(lr, c.Risk)
		}
	}
	h.mu.Unlock()
	h.trainMu.Lock()
	defer h.trainMu.Unlock()
	h.mu.Lock()
	if h.clfs != nil && h.clfs[scheme] != nil && h.clfK[scheme] == key { // another caller trained it meanwhile
		m := h.clfs[scheme]
		h.mu.Unlock()
		return m
	}
	h.mu.Unlock()
	m := &stopClassifier{trainLin(docs, ln), trainLin(docs, lc2), trainLin(docs, lr), len(docs)}
	h.mu.Lock()
	if h.clfs == nil {
		h.clfs, h.clfK = map[string]*stopClassifier{}, map[string]int{}
	}
	h.clfs[scheme], h.clfK[scheme] = m, key
	h.mu.Unlock()
	return m
}

func (h *hubStore) classifier() *stopClassifier { return h.classifierFor("") }

var envRe = regexp.MustCompile(`(?i)(spill|leak(age)? of (oil|fuel|mud|diesel)|pollut|waste|d[eé]chet|d[eé]versement|fuite d|environment|environnement|oil on (the )?ground|contamin|burn pit|تسرب|نفايات|تلوث|بيئ)`)

// classify fills Nature / Class / Risk. Rules for environment first, then the learned model.
func (c *stopClassifier) classify(card StopCard) (nature, class, risk string, conf float64) {
	toks := stopText(card)
	nature, nc := c.nature.predict(toks)
	class, cc := c.class.predict(toks)
	risk, rc := c.risk.predict(toks)
	conf = math.Min(nc, cc)
	if envRe.MatchString(card.Desc) {
		class = "Environnement"
		if nature != "Positive observation" {
			nature = "Environment"
		}
		cc = 0.9
		conf = math.Min(nc, 0.9)
	}
	_ = rc
	if lc(card.Action) == "good practice" || strings.Contains(lc(card.Desc), "good practice") {
		nature = "Positive observation"
	}
	return
}

// ----------------------------------------------------------------------------- generation

// expandTokens adds glossary equivalents (ar/en/fr/zh) of the description tokens.
func expandTokens(s string) map[string]float64 {
	gl := glossary()
	out := map[string]float64{}
	for _, t := range tokenize(s) {
		out[t] = 1
		for _, a := range gl[t] {
			if _, ok := out[a]; !ok {
				out[a] = 0.7
			}
		}
	}
	return out
}

type scoredStop struct {
	c StopCard
	s float64
}

func (h *hubStore) relevantStops(activity string, nature string, limit int) []scoredStop {
	q := expandTokens(activity)
	h.mu.Lock()
	defer h.mu.Unlock()
	df := map[string]float64{}
	docs := make([]map[string]bool, len(h.h.Stops))
	for i, c := range h.h.Stops {
		m := map[string]bool{}
		for _, t := range stopText(c) {
			m[t] = true
		}
		docs[i] = m
		for t := range m {
			df[t]++
		}
	}
	N := float64(len(h.h.Stops)) + 1
	var res []scoredStop
	for i, c := range h.h.Stops {
		if nature != "" && !strings.EqualFold(c.Nature, nature) {
			continue
		}
		var s float64
		for t, w := range q {
			if docs[i][t] {
				s += w * math.Log(1+N/(df[t]+1))
			}
		}
		if s > 0 {
			res = append(res, scoredStop{c, s})
		}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].s > res[j].s })
	// keep only cards close to the best match (a lone generic word like "operation" must not drag in unrelated cards)
	if len(res) > 0 {
		cut := res[0].s * 0.6
		for i, r := range res {
			if r.s < cut {
				res = res[:i]
				break
			}
		}
	}
	// drop near-duplicate descriptions
	var out []scoredStop
	seen := map[string]bool{}
	for _, r := range res {
		k := strings.Join(firstN(tokenize(r.c.Desc), 6), " ")
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
