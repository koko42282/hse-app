package main

import (
	"fmt"
	"strings"
)

// analysisSheet builds counts (COUNTIF formulas) by Nature / Classification / Risk / Status with charts.
func analysisSheet(log string, first, last int, natCol, classCol, riskCol, statusCol string) XSheet {
	sh := XSheet{Name: "Analysis", Widths: map[int]int{0: 28, 1: 10}}
	sh.Rows = append(sh.Rows, []XCell{xt("STOP Cards Analysis", 3)}, nil)
	block := func(title, col string, vals []string) (int, int) {
		sh.Rows = append(sh.Rows, []XCell{xt(title, 1), xt("Number", 1)})
		start := len(sh.Rows) + 1
		for _, v := range vals {
			r := len(sh.Rows) + 1
			sh.Rows = append(sh.Rows, []XCell{xt(v, 2), xf(fmt.Sprintf("COUNTIF('%s'!%s%d:%s%d,A%d)", log, col, first, col, last, r), 4)})
		}
		end := len(sh.Rows)
		sh.Rows = append(sh.Rows, []XCell{xt("Total", 6), xf(fmt.Sprintf("SUM(B%d:B%d)", start, end), 6)}, nil)
		return start, end
	}
	s1, e1 := block("Nature", natCol, []string{"Unsafe Act", "Unsafe Condition", "Positive observation", "Environment"})
	s2, e2 := block("Classification", classCol, stopClasses)
	s3, e3 := block("Risk level", riskCol, []string{"High", "Medium", "Low"})
	_, _ = block("Status", statusCol, []string{"Open", "Closed"})
	sh.Charts = append(sh.Charts,
		XChart{Type: "pie", Title: "Nature", Cats: fmt.Sprintf("A%d:A%d", s1, e1), Series: []XSeries{{Name: "Nature", Ref: fmt.Sprintf("B%d:B%d", s1, e1)}}, From: [2]int{3, 1}, To: [2]int{9, 16}},
		XChart{Type: "col", Title: "Classification", Cats: fmt.Sprintf("A%d:A%d", s2, e2), Series: []XSeries{{Name: "Cards", Ref: fmt.Sprintf("B%d:B%d", s2, e2)}}, From: [2]int{10, 1}, To: [2]int{18, 16}},
		XChart{Type: "col", Title: "Risk level", Cats: fmt.Sprintf("A%d:A%d", s3, e3), Series: []XSeries{{Name: "Cards", Ref: fmt.Sprintf("B%d:B%d", s3, e3)}}, From: [2]int{3, 17}, To: [2]int{9, 32}})
	return sh
}

func buildStopSonatrach(cards []StopCard, title string) []byte {
	if title == "" {
		title = "Stop Card Log"
	}
	sh := XSheet{Name: "Appareil", Freeze: "A3", Widths: map[int]int{0: 11, 1: 50, 2: 40, 3: 18, 4: 14, 5: 18, 6: 22, 7: 12, 8: 13, 9: 16, 10: 9, 11: 22}}
	sh.Rows = append(sh.Rows, []XCell{xt(title, 3)})
	hdr := []string{"Date", "Description of the Event", "Immediate Action / Remedial Action", "Fonction du rédacteur", "Zone", "Nature", "Classification", "Niveau de risque", "Echéancier", "Résponsable", "Etat", "Rédacteur"}
	var h []XCell
	for _, s := range hdr {
		h = append(h, xt(s, 7))
	}
	sh.Rows = append(sh.Rows, h)
	prevDate := ""
	for _, c := range cards {
		risk := c.Risk
		if risk == "" {
			risk = "NA"
		}
		dst := 2
		if c.Date != prevDate { // first card of a new day: yellow date cell = day separator
			dst = 10
			prevDate = c.Date
		}
		dead := c.Deadline
		if dead == "" {
			dead = map[string]string{"High": "Immediatement", "Medium": "1 jour", "Low": "3 jours"}[c.Risk]
		}
		resp := c.Responsible
		if resp == "" {
			resp = "SV HSE"
		}
		sh.Rows = append(sh.Rows, []XCell{xt(c.Date, dst), xt(c.Desc, 2), xt(c.Action, 2), xt(c.Function, 2), xt(c.Zone, 2), xt(c.Nature, 2), xt(c.Class, 2), xt(risk, 4), xt(dead, 2), xt(resp, 2), xt(orStr(c.Status, "Closed"), 4), xt(c.Observer, 2)})
	}
	last := len(sh.Rows)
	an := analysisSheet("Appareil", 3, last, "F", "G", "H", "K")
	return buildBook([]XSheet{sh, an})
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var oscoDefaultTag = map[string]string{"Réaction des Personnes": "Stopping job", "EPI": "Personal Protective Equipment (PPE)", "Position dangereuse": "Falling / Slips / Trips", "Procédures & Ordre": "Procedure not eatablished", "Outils & matériels": "Wrong for the job", "Autres": "Others", "Environnement": "Environment"}

func buildStopOSCO(cards []StopCard, title string) []byte {
	if title == "" {
		title = "STOP Cards Register"
	}
	sh := XSheet{Name: "Record", Freeze: "C5", Widths: map[int]int{0: 5, 1: 11, 2: 22, 3: 14, 4: 12, 5: 9, 6: 14, 7: 50, 8: 9, 9: 24, 10: 28, 11: 28, 12: 26, 13: 28, 14: 30, 15: 9}}
	sh.Rows = append(sh.Rows, []XCell{xt(title, 3)}, nil)
	hdr := []string{"No.", "Date", "Observer Name", "Post", "Company", "Risk Level", "Area (Observation Occurred)", "Observations", "Positive", "Reactions of people", "Personal Protective Equipment (PPE)", "Positions of people (Injury Causes)", "Procedures and Housekeeping", "Tools and Equipment", "Immediate (Corrective) Action(s) Taken", "Open/Closed"}
	var h []XCell
	for _, s := range hdr {
		h = append(h, xt(s, 7))
	}
	sh.Rows = append(sh.Rows, h)
	sh.Rows = append(sh.Rows, nil)
	// column per class
	colOf := map[string]int{"Réaction des Personnes": 9, "EPI": 10, "Position dangereuse": 11, "Procédures & Ordre": 12, "Outils & matériels": 13, "Autres": 13, "Environnement": 12}
	for i, c := range cards {
		row := make([]XCell, 16)
		d := c.Date
		if len(d) >= 7 {
			d = strings.ReplaceAll(d[:7], "-", ".")
		}
		row[0], row[1], row[2], row[3], row[4] = xt(fmt.Sprint(i+1), 4), xt(d, 4), xt(c.Observer, 2), xt(c.Function, 2), xt(c.Company, 2)
		row[5], row[6], row[7] = xt(c.Risk, 4), xt(c.Zone, 2), xt(c.Desc, 2)
		for k := 8; k < 14; k++ {
			row[k] = xt("", 2)
		}
		if c.Nature == "Positive observation" {
			row[8] = xt("positive", 4)
		} else {
			tags := c.Tags
			if len(tags) == 0 && c.Class != "" {
				tags = []string{oscoDefaultTag[c.Class]}
			}
			if len(tags) > 0 {
				col := colOf[c.Class]
				if col == 0 {
					col = 13
				}
				row[col] = xt(strings.Join(tags, "; "), 2)
			}
		}
		row[14] = xt(c.Action, 2)
		row[15] = xt(orStr(c.Status, "Closed"), 4)
		sh.Rows = append(sh.Rows, row)
	}
	last := len(sh.Rows)
	tot := make([]XCell, 16)
	tot[0] = xt("Total", 6)
	for k := 8; k < 14; k++ {
		tot[k] = xf(fmt.Sprintf("COUNTA(%s5:%s%d)", colName(k), colName(k), last), 6)
	}
	sh.Rows[3] = tot
	// OSCO register has no Nature/Class columns: analysis by sub-category groups, status and risk
	an := XSheet{Name: "Analysis", Widths: map[int]int{0: 34, 1: 10}}
	an.Rows = append(an.Rows, []XCell{xt("STOP Cards Analysis", 3)}, nil, []XCell{xt("Describe", 1), xt("Number", 1)})
	items := []struct{ l, f string }{
		{"Positive", fmt.Sprintf("COUNTIF(Record!I5:I%d,\"positive\")", last)},
		{"Reactions of people", fmt.Sprintf("COUNTA(Record!J5:J%d)", last)},
		{"PPE", fmt.Sprintf("COUNTA(Record!K5:K%d)", last)},
		{"Positions of people", fmt.Sprintf("COUNTA(Record!L5:L%d)", last)},
		{"Procedures and Housekeeping", fmt.Sprintf("COUNTA(Record!M5:M%d)", last)},
		{"Tools and Equipment", fmt.Sprintf("COUNTA(Record!N5:N%d)", last)},
		{"Total Stop Cards", fmt.Sprintf("COUNTA(Record!H5:H%d)", last)},
		{"Open", fmt.Sprintf("COUNTIF(Record!P5:P%d,\"Open\")", last)},
		{"Closed", fmt.Sprintf("COUNTIF(Record!P5:P%d,\"Closed\")", last)},
		{"High risk", fmt.Sprintf("COUNTIF(Record!F5:F%d,\"High\")", last)},
		{"Medium risk", fmt.Sprintf("COUNTIF(Record!F5:F%d,\"Medium\")", last)},
		{"Low risk", fmt.Sprintf("COUNTIF(Record!F5:F%d,\"Low\")", last)},
	}
	for _, it := range items {
		an.Rows = append(an.Rows, []XCell{xt(it.l, 2), xf(it.f, 4)})
	}
	an.Charts = append(an.Charts, XChart{Type: "col", Title: "STOP cards by category", Cats: "A4:A9", Series: []XSeries{{Name: "Cards", Ref: "B4:B9"}}, From: [2]int{3, 1}, To: [2]int{11, 18}})
	return buildBook([]XSheet{sh, an})
}
