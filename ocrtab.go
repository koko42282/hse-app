package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OCR tab: the browser does the OCR (tesseract.js, offline); the server prepares office files and builds the saved formats.
func (a *app) ocrTabHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	// docx / pptx / xlsx: native text per page + the list of pictures of each page (fetched one by one, OCR'd by the browser)
	mux.HandleFunc("/api/hse/ocr/office", guard(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Query().Get("name"))
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".docx" && ext != ".pptx" && ext != ".xlsx" && ext != ".xlsm" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "نوع غير مدعوم"})
			return
		}
		root := filepath.Join(os.TempDir(), "usbai_ocr")
		_ = os.MkdirAll(root, 0o755)
		if ents, err := os.ReadDir(root); err == nil { // clean old sessions
			for _, e := range ents {
				if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 3*time.Hour {
					_ = os.RemoveAll(filepath.Join(root, e.Name()))
				}
			}
		}
		sid := newID()
		dir := filepath.Join(root, sid)
		_ = os.MkdirAll(dir, 0o755)
		fp := filepath.Join(dir, "f"+ext)
		out, err := os.Create(fp)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		_, err = io.Copy(out, http.MaxBytesReader(w, r.Body, 400<<20))
		out.Close()
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		var pages []pageText
		if ext == ".pptx" {
			pages, _ = extractPPTXx(fp, true)
		} else {
			pages, _ = extractText(fp)
		}
		sizes := map[string]uint64{}
		if zr, err := zip.OpenReader(fp); err == nil {
			for _, f := range zr.File {
				sizes[f.Name] = f.UncompressedSize64
			}
			zr.Close()
		}
		uses := map[string]int{}
		for _, p := range pages {
			seen := map[string]bool{}
			for _, m := range imgMarker.FindAllStringSubmatch(p.Text, -1) {
				n := strings.ReplaceAll(m[1], "\x02", ".")
				if !seen[n] {
					seen[n] = true
					uses[n]++
				}
			}
		}
		var pg []map[string]interface{}
		for _, p := range pages {
			var imgs []map[string]interface{}
			seen := map[string]bool{}
			for _, m := range imgMarker.FindAllStringSubmatch(p.Text, -1) {
				n := strings.ReplaceAll(m[1], "\x02", ".")
				if seen[n] || !imgExts[strings.ToLower(filepath.Ext(n))] || uses[n] > 2 && len(pages) > 3 || sizes[n] < 1500 || sizes[n] > 40<<20 {
					continue
				}
				seen[n] = true
				imgs = append(imgs, map[string]interface{}{"n": n, "label": filepath.Base(n), "kb": sizes[n] / 1024})
			}
			t := strings.TrimSpace(imgMarker.ReplaceAllString(p.Text, ""))
			t = strings.Map(func(r rune) rune {
				if r < 9 || (r > 13 && r < 32) {
					return -1
				}
				return r
			}, t)
			pg = append(pg, map[string]interface{}{"page": p.Page, "text": t, "images": imgs})
		}
		writeJSON(w, map[string]interface{}{"ok": true, "sid": sid, "pages": pg})
	}))
	mux.HandleFunc("/api/hse/ocr/img", guard(func(w http.ResponseWriter, r *http.Request) {
		sid := filepath.Base(r.URL.Query().Get("sid"))
		n := r.URL.Query().Get("n")
		if !(strings.HasPrefix(n, "word/media/") || strings.HasPrefix(n, "ppt/media/") || strings.HasPrefix(n, "xl/media/")) || !imgExts[strings.ToLower(filepath.Ext(n))] {
			http.NotFound(w, r)
			return
		}
		ents, _ := os.ReadDir(filepath.Join(os.TempDir(), "usbai_ocr", sid))
		for _, e := range ents {
			zr, err := zip.OpenReader(filepath.Join(os.TempDir(), "usbai_ocr", sid, e.Name()))
			if err != nil {
				continue
			}
			b, ok := zipFile(zr, n)
			zr.Close()
			if !ok {
				continue
			}
			ct := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp"}[strings.ToLower(filepath.Ext(n))]
			w.Header().Set("Content-Type", ct)
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	mux.HandleFunc("/api/hse/ocr/save", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Format string `json:"format"`
			Title  string `json:"title"`
			Pages  []struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			} `json:"pages"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&in) != nil || len(in.Pages) == 0 {
			http.Error(w, "bad request", 400)
			return
		}
		if in.Title == "" {
			in.Title = "OCR"
		}
		var data []byte
		switch in.Format {
		case "pptx":
			var md strings.Builder
			for _, p := range in.Pages {
				lines := strings.Split(strings.TrimSpace(p.Text), "\n")
				for i := 0; i < len(lines) || i == 0; i += 12 {
					t := p.Title
					if i > 0 {
						t += fmt.Sprintf(" (%d)", i/12+1)
					}
					md.WriteString("## " + t + "\n")
					for j := i; j < i+12 && j < len(lines); j++ {
						if s := strings.TrimSpace(lines[j]); s != "" {
							md.WriteString(s + "\n\n")
						}
					}
				}
			}
			data = buildPPTX(in.Title, md.String())
		case "docx":
			var md strings.Builder
			for _, p := range in.Pages {
				md.WriteString("## " + p.Title + "\n\n")
				for _, l := range strings.Split(p.Text, "\n") {
					if s := strings.TrimSpace(l); s != "" {
						md.WriteString(s + "\n\n")
					}
				}
			}
			data = buildDOCX(in.Title, md.String())
		case "xlsx":
			sh := XSheet{Name: "OCR", Widths: map[int]int{0: 14, 1: 100}}
			sh.Rows = append(sh.Rows, []XCell{xt("Page", 7), xt("Text", 7)})
			for _, p := range in.Pages {
				for _, l := range strings.Split(p.Text, "\n") {
					if s := strings.TrimSpace(l); s != "" {
						sh.Rows = append(sh.Rows, []XCell{xt(p.Title, 4), xt(s, 2)})
					}
				}
			}
			data = buildBook([]XSheet{sh})
		case "txt":
			var b strings.Builder
			for _, p := range in.Pages {
				b.WriteString("=== " + p.Title + " ===\n" + p.Text + "\n\n")
			}
			data = []byte("\xef\xbb\xbf" + b.String())
		default:
			http.Error(w, "format", 400)
			return
		}
		a.sendFile(w, "OCR_"+safeName(strings.TrimSuffix(in.Title, filepath.Ext(in.Title)))+"_"+time.Now().Format("20060102_1504")+"."+in.Format, data)
	}))
}
