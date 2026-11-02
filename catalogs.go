package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Catalogs: <drive>\Catalogs\<topic>\files (pdf / docx / pptx / xlsx ...). Read-only in the program + full-text search with sources.
func (a *app) catDir() string {
	d := filepath.Join(a.driveDir(), "Catalogs")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func (a *app) initCatalogs() {
	a.cat = newRag(a.catDir(), filepath.Join(a.driveDir(), "data", "catalogs_index.json"), a.logf)
	a.rag.extra = a.cat
	a.cat.reindex()
}

func (a *app) catalogHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	a.initCatalogs()
	mux.HandleFunc("/api/hse/cat/tree", guard(func(w http.ResponseWriter, r *http.Request) {
		root := a.catDir()
		type fl struct {
			Rel  string `json:"rel"`
			Name string `json:"name"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		}
		groups := map[string][]fl{}
		var order []string
		ents, _ := os.ReadDir(root)
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				groups[e.Name()] = []fl{}
				order = append(order, e.Name())
			}
		}
		sort.Strings(order)
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !supported(info.Name()) {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			top := "."
			if i := strings.Index(rel, "/"); i > 0 {
				top = rel[:i]
			}
			if _, ok := groups[top]; !ok {
				groups[top] = []fl{}
				order = append(order, top)
			}
			groups[top] = append(groups[top], fl{rel, info.Name(), strings.TrimPrefix(strings.ToLower(filepath.Ext(p)), "."), info.Size()})
			return nil
		})
		var out []map[string]interface{}
		for _, k := range order {
			sort.Slice(groups[k], func(i, j int) bool { return groups[k][i].Rel < groups[k][j].Rel })
			out = append(out, map[string]interface{}{"folder": k, "files": groups[k]})
		}
		writeJSON(w, map[string]interface{}{"dir": root, "folders": out, "status": a.cat.status()})
	}))
	mux.HandleFunc("/api/hse/cat/reindex", guard(func(w http.ResponseWriter, r *http.Request) {
		a.cat.reindex()
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	mux.HandleFunc("/api/hse/cat/mkdir", guard(func(w http.ResponseWriter, r *http.Request) {
		n := safeName(filepath.Base(r.URL.Query().Get("name")))
		if n == "" || n == "." {
			writeJSON(w, map[string]interface{}{"ok": false})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": os.MkdirAll(filepath.Join(a.catDir(), n), 0o755) == nil})
	}))
	mux.HandleFunc("/api/hse/cat/upload", guard(func(w http.ResponseWriter, r *http.Request) {
		folder := safeName(filepath.Base(r.URL.Query().Get("folder")))
		name := safeName(filepath.Base(r.URL.Query().Get("name")))
		if !supported(name) || folder == "" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "نوع الملف غير مدعوم"})
			return
		}
		dir := filepath.Join(a.catDir(), folder)
		_ = os.MkdirAll(dir, 0o755)
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, 200<<20))
		f.Close()
		a.cat.reindex()
		writeJSON(w, map[string]interface{}{"ok": err == nil})
	}))
	// read-only access: the original file (PDF/images shown by the browser) or its extracted text
	mux.HandleFunc("/api/hse/cat/file", guard(func(w http.ResponseWriter, r *http.Request) {
		full, ok := a.cat.safeRel(r.URL.Query().Get("rel"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, full)
	}))
	mux.HandleFunc("/api/hse/cat/view", guard(func(w http.ResponseWriter, r *http.Request) {
		full, ok := a.cat.safeRel(r.URL.Query().Get("rel"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		pages, err := extractText(full)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		var out []map[string]interface{}
		for _, p := range pages {
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, map[string]interface{}{"page": p.Page, "text": p.Text})
			}
		}
		writeJSON(w, map[string]interface{}{"ok": true, "pages": out})
	}))
	mux.HandleFunc("/api/hse/cat/search", guard(func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		folder := r.URL.Query().Get("folder")
		si := a.cat.searcher()
		var out []map[string]interface{}
		if si != nil && q != "" {
			seen := map[string]bool{}
			for _, h := range si.search(q, 40) {
				c := si.data.Chunks[h.Chunk]
				rel := si.data.Files[c.File].Rel
				if folder != "" && !strings.HasPrefix(rel, folder+"/") {
					continue
				}
				key := rel + "|" + itoa(c.Page)
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, map[string]interface{}{"file": rel, "page": c.Page, "score": h.Score, "text": c.Text})
				if len(out) >= 15 {
					break
				}
			}
		}
		writeJSON(w, map[string]interface{}{"hits": out, "status": a.cat.status()})
	}))
	_ = json.Marshal
}
