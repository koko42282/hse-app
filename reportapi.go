package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Daily report (Sonatrach HSE report): the page does the calculations and sends cell -> value;
// the server reads a previous report as the baseline and writes values into a copy, keeping its formatting.

func (a *app) reportDirs() []string {
	root := filepath.Dir(a.rag.dir)
	return []string{a.rag.dir, a.outputDir(), filepath.Join(root, "data", "templates"), a.docsRoot()}
}

var reportNameRe = regexp.MustCompile(`(?i)daily|report|rapport|rdh|hse`)

func (a *app) reportTemplates() []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range a.reportDirs() {
		_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			n := strings.ToLower(e.Name())
			if strings.HasSuffix(n, ".xlsx") && !strings.HasPrefix(n, "~") && reportNameRe.MatchString(n) && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		si, _ := os.Stat(out[i])
		sj, _ := os.Stat(out[j])
		if si == nil || sj == nil {
			return out[i] < out[j]
		}
		return si.ModTime().After(sj.ModTime())
	})
	return out
}

// resolveReport maps an id returned by the list (index into the current listing) to a path.
func (a *app) resolveReport(id string) (string, bool) {
	for _, p := range a.reportTemplates() {
		if filepath.Base(filepath.Dir(p))+"/"+filepath.Base(p) == id {
			return p, true
		}
	}
	return "", false
}

func (a *app) reportHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hub/report/list", guard(func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]string
		for _, p := range a.reportTemplates() {
			out = append(out, map[string]string{"id": filepath.Base(filepath.Dir(p)) + "/" + filepath.Base(p), "name": filepath.Base(p)})
		}
		writeJSON(w, map[string]interface{}{"files": out})
	}))
	mux.HandleFunc("/api/hub/report/upload", guard(func(w http.ResponseWriter, r *http.Request) {
		orig := filepath.Base(r.URL.Query().Get("name"))
		name := safeName(orig)
		if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "يجب أن يكون الملف .xlsx"})
			return
		}
		if reRepNo.MatchString(orig) {
			a.learnName("daily", orig)
		}
		if !reportNameRe.MatchString(name) {
			name = "report_" + name
		}
		dir := filepath.Join(filepath.Dir(a.rag.dir), "data", "templates")
		_ = os.MkdirAll(dir, 0o755)
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, 40<<20))
		f.Close()
		writeJSON(w, map[string]interface{}{"ok": err == nil, "id": "templates/" + name})
	}))
	mux.HandleFunc("/api/hub/report/read", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in)
		p, ok := a.resolveReport(in.ID)
		if !ok {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "الملف غير موجود"})
			return
		}
		zr, err := zip.OpenReader(p)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		defer zr.Close()
		shs := xlsxSheets(zr)
		if len(shs) == 0 {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "لا أوراق"})
			return
		}
		cells := map[string]string{}
		for _, row := range xlsxRows(zr, shs[0].File, xlsxShared(zr)) {
			for _, c := range row {
				cells[c.Ref] = c.Value
			}
		}
		writeJSON(w, map[string]interface{}{"ok": true, "sheet": shs[0].Name, "cells": cells})
	}))
	mux.HandleFunc("/api/hub/report/fill", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID    string            `json:"id"`
			Date  string            `json:"date"` // YYYY-MM-DD
			Cells map[string]string `json:"cells"`
			Sheet string            `json:"sheet"`
			Name  string            `json:"name"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		data, err := a.fillReport(in.ID, in.Cells, in.Sheet)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if in.Date != "" {
			a.hub.mu.Lock()
			a.hub.h.Reports[in.Date] = map[string]interface{}{"cells": in.Cells}
			_ = a.hub.saveLocked()
			a.hub.mu.Unlock()
		}
		name := "HSE_DAILY_REPORT.xlsx"
		if strings.TrimSpace(in.Name) != "" {
			name = safeName(in.Name)
		}
		a.sendFile(w, name, data)
	}))
}

var reportRefRe = regexp.MustCompile(`^[A-Z]{1,3}\d{1,4}$`)
var dvRe = regexp.MustCompile(`(?s)(<dataValidation [^>]*sqref="O25:O34"[^>]*>)\s*<formula1>[^<]*</formula1>`)

// readBase returns the first sheet of a previous report as cell -> value.
func (a *app) readBase(id string) (map[string]string, string, bool) {
	p, ok := a.resolveReport(id)
	if !ok {
		return nil, "", false
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, "", false
	}
	defer zr.Close()
	shs := xlsxSheets(zr)
	if len(shs) == 0 {
		return nil, "", false
	}
	cells := map[string]string{}
	for _, row := range xlsxRows(zr, shs[0].File, xlsxShared(zr)) {
		for _, c := range row {
			cells[c.Ref] = c.Value
		}
	}
	return cells, shs[0].Name, true
}

// fillReport writes cells into a copy of the previous report (formatting kept) and repairs known defects:
// the stray date in O20 and the broken (#REF!) status list in O25:O34.
func (a *app) fillReport(id string, cells map[string]string, sheetName string) ([]byte, error) {
	p, ok := a.resolveReport(id)
	if !ok {
		return nil, fmt.Errorf("report not found")
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	shs := xlsxSheets(zr)
	if len(shs) == 0 {
		return nil, fmt.Errorf("no sheet")
	}
	b, _ := zipFile(zr, shs[0].File)
	sheet := string(b)
	refs := make([]string, 0, len(cells))
	for k := range cells {
		if reportRefRe.MatchString(k) {
			refs = append(refs, k)
		}
	}
	sort.Strings(refs)
	for _, k := range refs {
		sheet = setXlsxCell(sheet, k, cells[k])
	}
	if _, set := cells["O20"]; !set {
		sheet = setXlsxCell(sheet, "O20", "")
	}
	sheet = dvRe.ReplaceAllString(sheet, `$1<formula1>"OPEN,CLOSE"</formula1>`)
	repl := map[string][]byte{shs[0].File: []byte(sheet)}
	if wb, ok := zipFile(zr, "xl/workbook.xml"); ok {
		ws := string(wb)
		if sheetName != "" {
			ws = strings.Replace(ws, `name="`+xmlEsc(shs[0].Name)+`"`, `name="`+xmlEsc(sheetName)+`"`, 1)
		}
		if strings.Contains(ws, "<calcPr") {
			ws = regexp.MustCompile(`<calcPr[^>]*/>`).ReplaceAllString(ws, `<calcPr calcId="191029" fullCalcOnLoad="1"/>`)
		} else {
			ws = strings.Replace(ws, "</workbook>", `<calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>`, 1)
		}
		repl["xl/workbook.xml"] = []byte(ws)
	}
	return rewriteZip(zr, repl)
}
