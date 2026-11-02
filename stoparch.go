package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// STOP archive: a folder on the flash drive. Every .xls/.xlsx report dropped there becomes part of the program's dictionary.
func (a *app) stopArchDir() string {
	d := filepath.Join(a.driveDir(), "STOP_Archive")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func (a *app) scanStopArchive() (files int, read, added int, errs []string) {
	dir := a.stopArchDir()
	ents, _ := os.ReadDir(dir)
	done := map[string]string{}
	_ = json.Unmarshal([]byte(a.setting("stopArchDone")), &done)
	for _, e := range ents {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".xls" && ext != ".xlsx") || strings.HasPrefix(e.Name(), "~$") {
			continue
		}
		st, err := e.Info()
		if err != nil {
			continue
		}
		sig := fmt.Sprintf("%d|%d", st.Size(), st.ModTime().Unix())
		if done[e.Name()] == sig {
			continue
		}
		cards, _, err := importStopFile(filepath.Join(dir, e.Name()), "")
		if err != nil {
			errs = append(errs, e.Name()+": "+err.Error())
			continue
		}
		ad, _ := a.hub.addStops(a.hub.cleanBatch(cards))
		files++
		read += len(cards)
		added += ad
		done[e.Name()] = sig
	}
	b, _ := json.Marshal(done)
	a.hub.mu.Lock()
	a.hub.h.Settings["stopArchDone"] = string(b)
	_ = a.hub.saveLocked()
	a.hub.mu.Unlock()
	return
}

func (a *app) stopArchHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/stoparch/scan", guard(func(w http.ResponseWriter, r *http.Request) {
		f, rd, ad, errs := a.scanStopArchive()
		writeJSON(w, map[string]interface{}{"ok": true, "files": f, "read": rd, "added": ad, "errors": errs, "dir": a.stopArchDir()})
	}))
	mux.HandleFunc("/api/hse/stoparch/list", guard(func(w http.ResponseWriter, r *http.Request) {
		ents, _ := os.ReadDir(a.stopArchDir())
		var names []string
		for _, e := range ents {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		a.hub.mu.Lock()
		n := len(a.hub.h.Stops)
		a.hub.mu.Unlock()
		writeJSON(w, map[string]interface{}{"dir": a.stopArchDir(), "files": names, "cards": n})
	}))
	// upload = copy into the archive folder, then import
	mux.HandleFunc("/api/hse/stoparch/add", guard(func(w http.ResponseWriter, r *http.Request) {
		name := safeName(filepath.Base(r.URL.Query().Get("name")))
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".xls" && ext != ".xlsx" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "الملف يجب أن يكون .xls أو .xlsx"})
			return
		}
		f, err := os.Create(filepath.Join(a.stopArchDir(), name))
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
		fl, rd, ad, errs := a.scanStopArchive()
		writeJSON(w, map[string]interface{}{"ok": len(errs) == 0, "files": fl, "read": rd, "added": ad, "errors": errs})
	}))
}

// ---- generation modes ----
var generalWords = strings.Fields("housekeeping tidy untidy clean cleaning dirty order orderly storage stored store stack stacked pile park parked parking vehicle vehicles car truck pickup speed seatbelt ppe helmet hardhat glasses goggles gloves uniform coverall boots vest waste garbage bin trash litter camp kitchen mess dining sign signage label walkway walkways trip slip floor stairs stair handrail light lighting extinguisher cable cables hose hoses smoking smoke water drain drainage toilet laundry office container containers pallet pallets chemical chemicals spill leak mask hygiene food lifting-manual manual back")
var drillWords = strings.Fields("drill drilling bop mud pipe pipes derrick rotary kelly casing tripping trip-tank crane hoist tong tongs slips pump pumps well cement cementing rigfloor h2s choke manifold bit bha stand stands topdrive top-drive elevator elevators wireline logging fishing pit pits shaker")

func wordsOf(s string) map[string]bool {
	m := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '-') }) {
		m[f] = true
		m[strings.TrimSuffix(f, "s")] = true
	}
	return m
}

func isGeneralReq(act string) bool {
	l := lc(act)
	for _, k := range []string{"عامة", "عامه", "عام", "general", "générale", "generale", "général", "通用", "一般"} {
		if l == k || strings.HasPrefix(l, k+" ") || strings.HasPrefix(l, k+",") || strings.HasPrefix(l, k+":") {
			return true
		}
	}
	return false
}

// generalStops: dictionary cards about order, cleanliness, PPE, parking... and nothing about drilling operations.
func (a *app) generalStops() []StopCard {
	a.hub.mu.Lock()
	all := append([]StopCard(nil), a.hub.h.Stops...)
	a.hub.mu.Unlock()
	gw, dw := map[string]bool{}, map[string]bool{}
	for _, w := range generalWords {
		gw[w] = true
	}
	for _, w := range drillWords {
		dw[w] = true
	}
	var out []StopCard
	for _, c := range all {
		low := " " + strings.ToLower(c.Desc+" "+c.Action) + " "
		skip := false
		for _, st := range []string{"substruct", "floor", "crane", "lifting", "basket", "casing", "driller", "derrick", "mixing", " test", "cellar", "sling", "hoist", "bop", "mud", "pipe", "h2s", "pump", "kelly", "tong", "welding", "grinding", "cutting", "kill line", "choke", "manifold"} {
			if strings.Contains(low, st) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		ws := wordsOf(c.Desc + " " + c.Action)
		g, d := 0, 0
		for w := range ws {
			if gw[w] {
				g++
			}
			if dw[w] {
				d++
			}
		}
		if g > 0 && d == 0 {
			out = append(out, c)
		}
	}
	return out
}
