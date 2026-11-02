package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *app) photoBytes(kind string, r Rec) [][]byte {
	var out [][]byte
	for _, f := range r.L("photos") {
		b, err := os.ReadFile(filepath.Join(a.docsRoot(), "Photos", kind, safeName(r.S("id")), safeName(f)))
		if err == nil {
			out = append(out, b)
		}
	}
	return out
}

func (a *app) personNames(ids []string) []Person {
	a.hub.mu.Lock()
	defer a.hub.mu.Unlock()
	var out []Person
	for _, id := range ids {
		for _, p := range a.hub.h.People {
			if p.ID == id {
				out = append(out, p)
			}
		}
	}
	return out
}

func hm(s string) string {
	if len(s) == 5 && s[2] == ':' {
		return s[:2] + " H " + s[3:]
	}
	return s
}

var formW = []int{2500, 2600, 2500, 2600}

func (a *app) docIncident(r Rec) []byte {
	d := newDx(false)
	rig, _ := a.rigWell()
	d.table([]int{1500, 5200, 3500}, [][]dxCell{{{Lines: []string{"SONATRACH"}, Bold: true, Size: 22, Align: "center"}, head("Compte-rendu d’accident / incident", 1), {Lines: []string{"Rig: " + rig}, Align: "center"}}})
	site := firstOf(r.S("fr_site"), rig)
	d.table([]int{5100, 5100}, [][]dxCell{
		{txt("2. Site : " + site), txt("3. Lieu de l’accident/incident : " + r.S("fr_lieu"))},
		{txt("4. Date de l’accident/incident : " + strings.ReplaceAll(dmy(r.S("date")), "-", "/")), txt("5. Heure de l’accident/incident : " + r.S("time"))},
	})
	d.table([]int{10200}, [][]dxCell{
		{{Lines: append([]string{"6. Description du lieu et des installations impliquées :"}, strings.Split(r.S("fr_lieudesc"), "\n")...)}},
		{{Lines: append([]string{"Brève description de l'événement :"}, strings.Split(r.S("fr_event"), "\n")...)}},
	})
	d.table([]int{3400, 3400, 3400}, [][]dxCell{
		{lbl("Aux personnes"), lbl("A l’environnement"), lbl("Aux biens")},
		{txt(firstOf(r.S("fr_persons"), "Néant")), txt(firstOf(r.S("fr_env"), "Néant")), txt(firstOf(r.S("fr_assets"), "Néant"))},
	})
	d.table([]int{5100, 5100}, [][]dxCell{
		{{Lines: []string{"10. Etablissement du CR", "Le " + strings.ReplaceAll(dmy(firstOf(r.S("fr_cr_date"), r.S("date"))), "-", "/") + "  —  " + a.setting("hseName")}}, {Lines: []string{"11. Validation du CR", "(Nom, date, visa)", r.S("fr_valid")}}},
	})
	if ph := a.photoBytes("incident", r); len(ph) > 0 {
		d.para("Photos :", true, 22, "")
		d.photoGrid([]int{5100, 5100}, ph)
	}
	return d.bytes()
}

func numbered(prefix string, l []string) []string { return l }

func (a *app) docDrill(r Rec) []byte {
	d := newDx(false)
	rig, well := a.rigWell()
	typ := map[string]string{"kick": "KICK DRILL", "kick_drilling": "KICK DRILL (DRILLING)", "fire": "FIRE DRILL", "medevac": "MEDEVAC DRILL", "security": "SECURITY DRILL", "environment": "ENVIRONMENT (SPILL) DRILL", "other": "DRILL"}[r.S("type")]
	d.para("Compte-rendu d’exercice de simulation", true, 28, "center")
	end := r.S("end")
	rows := [][]dxCell{
		{lbl("Situation d’urgence:"), txt(firstOf(r.S("situation"), typ)), lbl("DATE:"), txt(strings.ReplaceAll(dmy(r.S("date")), "-", "/"))},
		{lbl("Chantier:"), txt(firstOf(r.S("chantier"), rig)), lbl("Heure de début:"), txt(hm(r.S("start")))},
		{lbl("Puits:"), txt(firstOf(r.S("puits"), well)), lbl("Heure de fin:"), txt(hm(end))},
		{lbl("Leaders :"), txt(r.S("leaders")), lbl("Temps de l’exercice:"), txt(firstOf(r.S("duration"), fmt.Sprintf("%g min", r.N("minutes"))))},
	}
	d.table(formW, rows)
	full := []int{10200}
	d.table(full, [][]dxCell{{head("Objectifs de l’exercice", 1)}, {{Lines: strings.Split(r.S("objectives"), "\n")}}, {{Lines: strings.Split("Scénario: "+r.S("scenario"), "\n")}}})
	d.table([]int{3600, 6600}, [][]dxCell{
		{lbl("Quoi/ où l’urgence s’est déclenchée?"), txt(r.S("qa_what"))},
		{lbl("Qui a signalé l’urgence?"), txt(r.S("qa_who"))},
		{lbl("Comment l’urgence s’est déclenchée"), txt(r.S("qa_how"))},
	})
	chron := [][]dxCell{{lbl("Chronologie:")}}
	for _, l := range r.L("chrono") {
		chron = append(chron, []dxCell{txt(l)})
	}
	d.table(full, chron)
	d.table(full, [][]dxCell{{{Lines: strings.Split("3-Points positifs: "+r.S("positives"), "\n")}}, {{Lines: strings.Split("4-Actions à améliorer: "+r.S("improvements"), "\n")}}, {lbl("5-Conclusion et recommandations : " + r.S("conclusion"))}})
	acts := [][]dxCell{{lbl("Action"), lbl("Leader"), lbl("Échéancier")}}
	for _, l := range r.L("actions") { // "action | leader | deadline"
		p := strings.Split(l, "|")
		for len(p) < 3 {
			p = append(p, "")
		}
		acts = append(acts, []dxCell{txt(strings.TrimSpace(p[0])), txt(strings.TrimSpace(p[1])), txt(strings.TrimSpace(p[2]))})
	}
	d.table([]int{5200, 2600, 2400}, acts)
	d.table([]int{3400, 3400, 3400}, [][]dxCell{
		{head("6-Approbation", 3)},
		{lbl("Superviseur HSE"), lbl("Chef du chantier"), lbl("Superviseur Forage")},
		{txt(firstOf(r.S("ap_hse"), a.setting("hseName"))), txt(r.S("ap_site")), txt(r.S("ap_drill"))},
	})
	if ph := a.photoBytes("drill", r); len(ph) > 0 {
		d.para("7-Photos:", true, 22, "")
		d.photoGrid([]int{5100, 5100}, ph)
	}
	if n := r.L("attendeeNames"); len(n) > 0 {
		d.para("Participants ("+fmt.Sprint(len(n))+"): "+strings.Join(n, ", "), false, 18, "")
	}
	return d.bytes()
}

func (a *app) docSheet(kind string, r Rec) []byte { // induction / training attendance sheets (FR + AR labels)
	d := newDx(false)
	rig, well := a.rigWell()
	title := map[string]string{"induction": "Fiche d’induction HSE / استمارة التعريف بالسلامة", "training": "Fiche de présence — Formation / ورقة حضور تدريب", "greenday": "Green Day — Compte-rendu", "inspection": "Rapport d’inspection / تقرير تفتيش"}[kind]
	d.table([]int{10200}, [][]dxCell{{head(title, 1)}})
	t := firstOf(r.S("title"), r.S("topic"))
	d.table(formW, [][]dxCell{
		{lbl("Date"), txt(strings.ReplaceAll(dmy(r.S("date")), "-", "/")), lbl("Chantier / Puits"), txt(rig + " / " + well)},
		{lbl("Début / Fin"), txt(hm(r.S("start")) + " → " + hm(r.S("end"))), lbl("Durée (min)"), txt(fmt.Sprint(r.N("minutes")))},
		{lbl("Formateur / Animateur"), txt(firstOf(r.S("trainer"), a.setting("hseName"))), lbl("Thème"), txt(t)},
	})
	if kind == "induction" {
		var topics []string
		_ = topics
		d.para("Programme de l’induction :", true, 22, "")
		for _, s := range r.L("outline") {
			d.para("• "+s, false, 20, "")
		}
	}
	if notes := firstOf(r.S("notes"), r.S("findings"), r.S("activities")); notes != "" {
		d.table([]int{10200}, [][]dxCell{{lbl("Notes / Constats")}, {{Lines: strings.Split(notes, "\n")}}})
	}
	ids := r.L("people")
	if len(ids) == 0 {
		ids = r.L("attendees")
	}
	if ps := a.personNames(ids); len(ps) > 0 {
		rows := [][]dxCell{{lbl("N°"), lbl("Nom et prénom"), lbl("Fonction"), lbl("Société"), lbl("Signature")}}
		for i, p := range ps {
			rows = append(rows, []dxCell{txt(fmt.Sprint(i + 1)), txt(p.Name), txt(p.Function), txt(p.Company), txt("")})
		}
		d.table([]int{700, 3600, 2200, 2100, 1600}, rows)
	}
	if ph := a.photoBytes(kind, r); len(ph) > 0 {
		d.para("Photos :", true, 22, "")
		d.photoGrid([]int{5100, 5100}, ph)
	}
	return d.bytes()
}

func (a *app) docFor(kind string, r Rec) ([]byte, string) {
	var data []byte
	if kind != "incident" { // incident fields are already French (AI helper); the others follow the report language
		r, _ = a.trRec(r, a.reportLang())
	}
	switch kind {
	case "incident":
		data = a.docIncident(r)
	case "drill":
		data = a.docDrill(r)
	default:
		data = a.docSheet(kind, r)
	}
	name := fmt.Sprintf("%s_%s_%s.docx", map[string]string{"incident": "Compte-rendu_accident_incident", "drill": "Compte-rendu_exercice_" + r.S("type"), "induction": "Induction", "training": "Training", "greenday": "GreenDay", "inspection": "Inspection"}[kind], strings.ReplaceAll(dmy(r.S("date")), "-", ""), firstOf(r.S("id"), "x")[:min(4, len(firstOf(r.S("id"), "x")))])
	return data, safeName(name)
}

func (a *app) formHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/doc", guard(func(w http.ResponseWriter, r *http.Request) {
		kind, id := r.URL.Query().Get("kind"), r.URL.Query().Get("id")
		for _, x := range a.hub.recsOf(kind) {
			if x.S("id") == id {
				if ps := a.personNames(append(x.L("attendees"), x.L("people")...)); len(ps) > 0 && kind == "drill" {
					var n []interface{}
					for _, p := range ps {
						n = append(n, p.Name)
					}
					x["attendeeNames"] = n
				}
				data, name := a.docFor(kind, x)
				a.sendFile(w, name, data)
				return
			}
		}
		http.NotFound(w, r)
	}))
}

var _ = time.Now
