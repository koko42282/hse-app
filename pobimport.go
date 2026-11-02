package main

import (
	"archive/zip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var colRe = regexp.MustCompile(`^([A-Z]+)(\d+)$`)

type pobRow struct{ Name, Function, Company string }

// parsePOBXlsx reads a Sonatrach-style POB list: company header rows (name in column B only), then numbered people rows.
func parsePOBXlsx(path string) ([]pobRow, string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, "", err
	}
	defer zr.Close()
	shs := xlsxSheets(zr)
	if len(shs) == 0 {
		return nil, "", io.ErrUnexpectedEOF
	}
	sh := xlsxShared(zr)
	var out []pobRow
	date, company := "", ""
	for _, row := range xlsxRows(zr, shs[0].File, sh) {
		cols := map[string]string{}
		rn := 0
		for _, c := range row {
			if m := colRe.FindStringSubmatch(c.Ref); m != nil {
				cols[m[1]] = strings.TrimSpace(c.Value)
				rn, _ = strconv.Atoi(m[2])
			}
		}
		if date == "" && rn <= 4 {
			if d := normDateAny(cols["D"]); d != "" && len(cols["D"]) > 4 {
				date = d
			}
		}
		_, numErr := strconv.Atoi(strings.TrimSuffix(cols["A"], ".0"))
		switch {
		case numErr == nil && cols["B"] != "":
			co := company
			if co == "" {
				co = strings.TrimSpace(cols["D"])
			}
			out = append(out, pobRow{normSpaces(cols["B"]), normSpaces(cols["C"]), normSpaces(co)})
		case cols["A"] == "" && cols["B"] != "" && cols["C"] == "" && rn > 6 && !strings.EqualFold(cols["B"], "nom et prénom"):
			company = normSpaces(cols["B"])
		}
	}
	return out, date, nil
}

// mergePOB adds new people, updates function/company of known ones, optionally marks absentees as departed.
func (a *app) mergePOB(rows []pobRow, date string, sync bool) (added, updated, left int) {
	h := a.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	when := time.Now().Format("2006-01-02T15:04")
	if date != "" {
		when = date + "T00:00"
	}
	idx := map[string]int{}
	for i, p := range h.h.People {
		idx[lc(normSpaces(p.Name))] = i
	}
	seen := map[int]bool{}
	for _, r := range rows {
		k := lc(r.Name)
		if i, ok := idx[k]; ok {
			seen[i] = true
			p := &h.h.People[i]
			ch := false
			if r.Function != "" && p.Function != r.Function {
				p.Function, ch = r.Function, true
			}
			if r.Company != "" && p.Company != r.Company {
				p.Company, ch = r.Company, true
			}
			if !p.onBoardNow() {
				p.Stays = append(p.Stays, Stay{In: when})
				ch = true
			}
			if ch {
				updated++
			}
			continue
		}
		p := Person{ID: newID(), Name: r.Name, Function: r.Function, Company: r.Company, Added: time.Now().Format("2006-01-02"), Stays: []Stay{{In: when}}}
		h.h.People = append(h.h.People, p)
		idx[k] = len(h.h.People) - 1
		seen[idx[k]] = true
		added++
	}
	if sync {
		now := time.Now().Format("2006-01-02T15:04")
		for i := range h.h.People {
			p := &h.h.People[i]
			if !seen[i] && p.onBoardNow() {
				if len(p.Stays) == 0 {
					p.Stays = []Stay{{In: p.Added + "T00:00"}}
				}
				p.Stays[len(p.Stays)-1].Out = now
				left++
			}
		}
	}
	_ = h.saveLocked()
	return
}

// driveDir = the root of the flash drive (next to AI_CORE).
func (a *app) driveDir() string { return filepath.Dir(filepath.Dir(a.hub.path)) }

func (a *app) pobImportHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/pob/import", guard(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Query().Get("name"))
		if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "الملف يجب أن يكون .xlsx"})
			return
		}
		tmp, err := os.CreateTemp("", "pob*.xlsx")
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		defer os.Remove(tmp.Name())
		_, err = io.Copy(tmp, http.MaxBytesReader(w, r.Body, 40<<20))
		tmp.Close()
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		rows, date, err := parsePOBXlsx(tmp.Name())
		if err != nil || len(rows) == 0 {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "لم أجد أشخاصاً في الملف (يُتوقع جدول POB: العنوان باسم الشركة، ثم أسطر مرقّمة)"})
			return
		}
		added, upd, left := a.mergePOB(rows, date, r.URL.Query().Get("sync") == "1")
		a.learnName("pob", name)
		a.syncPeopleFile()
		writeJSON(w, map[string]interface{}{"ok": true, "read": len(rows), "added": added, "updated": upd, "left": left, "date": date, "master": a.peopleMasterPath()})
	}))
}
