package main

// HSE data hub: local JSON database (people / POB, STOP cards, daily-report history) stored
// on the drive in <root>\data\hse.json.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Person struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Function    string  `json:"function"`
	Category    string  `json:"category"`
	Birth       string  `json:"birth"`
	Chronic     bool    `json:"chronic"`
	ChronicNote string  `json:"chronicNote,omitempty"`
	Company     string  `json:"company"`
	Crew        string  `json:"crew,omitempty"`
	Shift       string  `json:"shift,omitempty"` // Day | Night
	Phone       string  `json:"phone,omitempty"`
	Photo       string  `json:"photo,omitempty"`  // small JPEG data URL
	Stays       []Stay  `json:"stays,omitempty"`  // arrivals / departures (archive)
	Arrive      string  `json:"arrive,omitempty"` // input only: arrival date-time of a new person
	Added       string  `json:"added,omitempty"`
	Inducted    string  `json:"inducted,omitempty"` // date of the HSE site induction
	Daily       float64 `json:"daily,omitempty"`    // daily wage (salary hook for the pointage)
	Monthly     float64 `json:"monthly,omitempty"`  // monthly income (salary hook)
}

type StopCard struct {
	ID          string   `json:"id"`
	Date        string   `json:"date"` // YYYY-MM-DD
	Desc        string   `json:"desc"`
	Action      string   `json:"action"`
	Observer    string   `json:"observer"`
	Function    string   `json:"function"`
	Company     string   `json:"company"`
	Zone        string   `json:"zone"`
	Nature      string   `json:"nature"` // Unsafe Act | Unsafe Condition | Positive observation
	Class       string   `json:"class"`  // EPI | Position dangereuse | Outils & matériels | Procédures & Ordre | Réaction des Personnes | Autres
	Risk        string   `json:"risk"`   // High | Medium | Low | NA
	Deadline    string   `json:"deadline"`
	Responsible string   `json:"responsible"`
	Status      string   `json:"status"` // Open | Closed
	Source      string   `json:"source,omitempty"`
	Tags        []string `json:"tags,omitempty"`      // sub-categories as written in the register
	Verified    bool     `json:"verified,omitempty"`  // checked/corrected by the user: weighs more when the classifier learns
	Fixed       []string `json:"fixed,omitempty"`     // what the cleaner changed: text | class | nature | risk
	Orig        []string `json:"orig,omitempty"`      // original [desc, action, class, nature] before cleaning (only when something changed)
	Src         string   `json:"src,omitempty"`       // source report file name
	Scheme      string   `json:"scheme,omitempty"`    // sonatrach | sinopec: which company model the card belongs to
	Pred        []string `json:"pred,omitempty"`      // what the model suggested [nature,class,risk] when the user typed the card
	Confirmed   bool     `json:"confirmed,omitempty"` // user accepted / corrected the suggestion
}

type Hub struct {
	People   []Person                          `json:"people"`
	Stops    []StopCard                        `json:"stops"`
	Reports  map[string]map[string]interface{} `json:"reports"` // daily report inputs by date
	Settings map[string]string                 `json:"settings"`
	Recs     map[string][]Rec                  `json:"recs"` // ops, ptw, training, drill, inspection, incident, greenday, meeting...
}

type hubStore struct {
	mu      sync.Mutex
	path    string
	h       Hub
	clf     *stopClassifier
	clfN    int
	clfs    map[string]*stopClassifier // per company-scheme models
	trainMu sync.Mutex
	clfK    map[string]int
	ver     int // bumped when cards are edited so the classifier retrains
}

func newHub(path string) *hubStore {
	s := &hubStore{path: path}
	s.h.Reports = map[string]map[string]interface{}{}
	s.h.Settings = map[string]string{}
	s.h.Recs = map[string][]Rec{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.h)
		if s.h.Reports == nil {
			s.h.Reports = map[string]map[string]interface{}{}
		}
		if s.h.Recs == nil {
			s.h.Recs = map[string][]Rec{}
		}
		if s.h.Settings == nil {
			s.h.Settings = map[string]string{}
		}
	}
	return s
}

func (s *hubStore) saveLocked() error {
	b, err := json.MarshalIndent(&s.h, "", " ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	// keep one backup
	if _, err := os.Stat(s.path); err == nil {
		_ = os.Rename(s.path, s.path+".bak")
	}
	return os.Rename(tmp, s.path)
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func normSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

func (s *hubStore) companies() []map[string]interface{} {
	cnt := map[string]int{}
	chronic := map[string]int{}
	for _, p := range s.h.People {
		if !p.onBoardNow() {
			continue
		}
		c := p.Company
		if c == "" {
			c = "—"
		}
		cnt[c]++
		if p.Chronic {
			chronic[c]++
		}
	}
	var names []string
	for k := range cnt {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []map[string]interface{}
	for _, n := range names {
		out = append(out, map[string]interface{}{"company": n, "count": cnt[n], "chronic": chronic[n]})
	}
	return out
}

func (a *app) hubHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	h := a.hub
	mux.HandleFunc("/api/hub/people", guard(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		people := h.h.People
		if people == nil {
			people = []Person{}
		}
		on := 0
		for _, p := range people {
			if p.onBoardNow() {
				on++
			}
		}
		writeJSON(w, map[string]interface{}{"people": people, "companies": h.companies(), "total": on, "archived": len(people) - on, "settings": h.h.Settings})
	}))
	mux.HandleFunc("/api/hub/person", guard(func(w http.ResponseWriter, r *http.Request) {
		var p Person
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p) != nil || strings.TrimSpace(p.Name) == "" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "الاسم مطلوب"})
			return
		}
		p.Name, p.Function, p.Category, p.Company, p.Crew = normSpaces(p.Name), normSpaces(p.Function), normSpaces(p.Category), normSpaces(p.Company), normCrew(p.Crew)
		p.Phone, p.Shift = normSpaces(p.Phone), normShift(p.Shift)
		if len(p.Photo) > 60000 || !strings.HasPrefix(p.Photo, "data:image/") {
			p.Photo = ""
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if p.ID != "" {
			for i := range h.h.People {
				if h.h.People[i].ID == p.ID {
					p.Added = h.h.People[i].Added
					p.Stays = h.h.People[i].Stays
					p.Arrive = ""
					h.h.People[i] = p
					_ = h.saveLocked()
					writeJSON(w, map[string]interface{}{"ok": true, "id": p.ID})
					return
				}
			}
		}
		p.ID = newID()
		p.Added = time.Now().Format("2006-01-02")
		in := normWhen(p.Arrive)
		if in == "" {
			in = time.Now().Format("2006-01-02T15:04")
		}
		p.Arrive = ""
		p.Stays = []Stay{{In: in}}
		h.h.People = append(h.h.People, p)
		err := h.saveLocked()
		writeJSON(w, map[string]interface{}{"ok": err == nil, "id": p.ID})
	}))
	mux.HandleFunc("/api/hub/person/delete", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in)
		del := map[string]bool{}
		for _, id := range in.IDs {
			del[id] = true
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		var keep []Person
		for _, p := range h.h.People {
			if !del[p.ID] {
				keep = append(keep, p)
			}
		}
		h.h.People = keep
		_ = h.saveLocked()
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	// bulk import: pasted rows (tab / ; / , separated). Columns: Name, Function, Category, Birth, Chronic, Company
	mux.HandleFunc("/api/hub/people/import", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text    string `json:"text"`
			Company string `json:"company"` // default company for rows without one
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		added, skipped := h.importPeople(in.Text, in.Company)
		writeJSON(w, map[string]interface{}{"ok": true, "added": added, "skipped": skipped})
	}))
	mux.HandleFunc("/api/hub/settings", guard(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		for k, v := range in {
			h.h.Settings[k] = v
		}
		_ = h.saveLocked()
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
}

var colAlias = map[string][]string{
	"name":     {"name", "nom", "nom et prénom", "nom et prenom", "nom prénom", "full name", "الاسم", "اسم", "姓名", "名字"},
	"function": {"function", "fonction", "post", "poste", "position", "job", "الوظيفة", "المنصب", "岗位", "职位"},
	"category": {"category", "catégorie", "categorie", "cat", "الفئة", "صنف", "类别"},
	"birth":    {"birth", "date of birth", "dob", "date de naissance", "naissance", "تاريخ الميلاد", "الميلاد", "出生日期"},
	"chronic":  {"chronic", "maladie chronique", "chronic disease", "مرض مزمن", "مزمن", "慢性病"},
	"crew":     {"crew", "équipe", "equipe", "rotation", "الفريق", "فريق", "مناوبة", "班组"},
	"phone":    {"phone", "tel", "tél", "téléphone", "telephone", "mobile", "هاتف", "رقم الهاتف", "الهاتف", "电话"},
	"shift":    {"shift", "jour/nuit", "day/night", "نهار/ليل", "الدوام", "班次"},
	"company":  {"company", "société", "societe", "entreprise", "الشركة", "شركة", "公司"},
}

func colKind(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	for k, al := range colAlias {
		for _, a := range al {
			if h == a {
				return k
			}
		}
	}
	return ""
}

func splitRow(l string) []string {
	sep := "\t"
	if !strings.Contains(l, "\t") {
		if strings.Count(l, ";") >= 1 {
			sep = ";"
		} else if strings.Count(l, "|") >= 1 {
			sep = "|"
		} else {
			sep = ","
		}
	}
	var out []string
	for _, c := range strings.Split(l, sep) {
		out = append(out, strings.TrimSpace(c))
	}
	return out
}

func yes(s string) (bool, string) {
	l := strings.ToLower(strings.TrimSpace(s))
	switch l {
	case "", "no", "non", "n", "0", "لا", "否", "-", "none", "aucune", "néant":
		return false, ""
	case "yes", "oui", "y", "1", "نعم", "是":
		return true, ""
	}
	return true, strings.TrimSpace(s) // free text = the disease name
}

func (s *hubStore) importPeople(text, defCompany string) (added, skipped int) {
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	order := []string{"name", "function", "category", "birth", "chronic", "company", "crew"}
	s.mu.Lock()
	defer s.mu.Unlock()
	exists := map[string]bool{}
	for _, p := range s.h.People {
		exists[strings.ToLower(p.Name)+"|"+strings.ToLower(p.Company)] = true
	}
	first := true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		cells := splitRow(l)
		if first {
			first = false
			// header row?
			hdr := make([]string, len(cells))
			ok := 0
			for i, c := range cells {
				hdr[i] = colKind(c)
				if hdr[i] != "" {
					ok++
				}
			}
			if ok >= 2 {
				order = hdr
				continue
			}
		}
		var p Person
		for i, c := range cells {
			if i >= len(order) || order[i] == "" {
				continue
			}
			switch order[i] {
			case "name":
				p.Name = normSpaces(c)
			case "function":
				p.Function = normSpaces(c)
			case "category":
				p.Category = normSpaces(c)
			case "birth":
				p.Birth = normDate(c)
			case "chronic":
				p.Chronic, p.ChronicNote = yes(c)
			case "company":
				p.Company = normSpaces(c)
			case "crew":
				p.Crew = normCrew(c)
			case "phone":
				p.Phone = normSpaces(c)
			case "shift":
				p.Shift = normShift(c)
			}
		}
		if p.Company == "" {
			p.Company = defCompany
		}
		if p.Name == "" {
			skipped++
			continue
		}
		key := strings.ToLower(p.Name) + "|" + strings.ToLower(p.Company)
		if exists[key] {
			skipped++
			continue
		}
		exists[key] = true
		p.ID = newID()
		p.Added = time.Now().Format("2006-01-02")
		p.Stays = []Stay{{In: time.Now().Format("2006-01-02T15:04")}}
		s.h.People = append(s.h.People, p)
		added++
	}
	_ = s.saveLocked()
	return
}

// normDate accepts 2026-09-17, 17/09/2026, 17-09-2026, 17.09.2026 and returns YYYY-MM-DD (or the input).
func normDate(s string) string {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "02/01/2006", "2/1/2006", "02-01-2006", "02.01.2006", "2006/01/02", "01/02/2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	if len(s) >= 10 && s[4] == '-' {
		return s[:10]
	}
	return s
}

// normCrew turns "CREW A " / "crew a" / "فريق A" into "A".
func normCrew(s string) string {
	s = normSpaces(s)
	l := strings.ToLower(s)
	for _, p := range []string{"crew ", "équipe ", "equipe ", "فريق ", "team "} {
		if strings.HasPrefix(l, p) {
			return strings.ToUpper(strings.TrimSpace(s[len(p):]))
		}
	}
	if len(s) <= 3 {
		return strings.ToUpper(s)
	}
	return s
}
