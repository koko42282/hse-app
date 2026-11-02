package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (a *app) outputDir() string {
	d := filepath.Join(filepath.Dir(a.rag.dir), "output")
	_ = os.MkdirAll(d, 0o755)
	return d
}

var badName = regexp.MustCompile(`[\\/:*?"<>|\r\n\t]+`)

func safeName(s string) string {
	s = strings.TrimSpace(badName.ReplaceAllString(s, "_"))
	if s == "" {
		s = "file"
	}
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return s
}

func (a *app) sendFile(w http.ResponseWriter, name string, data []byte) {
	// keep a copy on the drive
	saved := filepath.Join(a.outputDir(), name)
	_ = os.WriteFile(saved, data, 0o644)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	w.Header().Set("X-Saved-As", url.PathEscape(saved))
	w.Header().Set("Access-Control-Expose-Headers", "X-Saved-As")
	_, _ = w.Write(data)
}

func (a *app) handleExport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Format string `json:"format"`
		Title  string `json:"title"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		http.Error(w, "bad request", 400)
		return
	}
	var data []byte
	switch in.Format {
	case "docx":
		data = buildDOCX(in.Title, in.Text)
	case "xlsx":
		data = buildXLSX(in.Title, in.Text)
	case "pptx":
		data = buildPPTX(in.Title, in.Text)
	default:
		http.Error(w, "format", 400)
		return
	}
	base := map[string]string{"docx": "UsbAi_report", "xlsx": "UsbAi_table", "pptx": "UsbAi_slides"}[in.Format]
	a.sendFile(w, base+"_"+time.Now().Format("20060102_150405")+"."+in.Format, data)
}

func (a *app) handleTplList(w http.ResponseWriter, r *http.Request) {
	si := a.rag.searcher()
	var out []map[string]interface{}
	_ = filepath.Walk(a.rag.dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		e := strings.ToLower(filepath.Ext(p))
		if (e == ".docx" || e == ".xlsx" || e == ".xlsm") && supported(info.Name()) {
			rel, _ := filepath.Rel(a.rag.dir, p)
			out = append(out, map[string]interface{}{"rel": filepath.ToSlash(rel)})
		}
		return nil
	})
	_ = si
	writeJSON(w, map[string]interface{}{"templates": out})
}

func (a *app) handleTplFields(w http.ResponseWriter, r *http.Request) {
	full, ok := a.rag.safeRel(r.URL.Query().Get("rel"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := analyzeTemplate(full)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "fields": f})
}

func (a *app) handleTplFill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rel    string                 `json:"rel"`
		Values map[string]interface{} `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	full, ok := a.rag.safeRel(in.Rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fillTemplate(full, in.Values)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ext := filepath.Ext(full)
	base := strings.TrimSuffix(filepath.Base(full), ext)
	a.sendFile(w, safeName(base)+"_"+time.Now().Format("20060102_150405")+ext, data)
}

// handleTplAutofill asks the local model to extract the values of the form fields from free text.
func (a *app) handleTplAutofill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rel  string `json:"rel"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		http.Error(w, "bad request", 400)
		return
	}
	a.mu.Lock()
	state, port, noThink := a.state, a.port, a.noThink
	a.mu.Unlock()
	if state != "running" {
		if a.modelOffNow() {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "MODEL_OFF"})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": false, "error": "شغّل النموذج أولاً"})
		return
	}
	full, ok := a.rag.safeRel(in.Rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	fields, err := analyzeTemplate(full)
	if err != nil || len(fields) == 0 {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "لا حقول في النموذج"})
		return
	}
	// unique key per field (labels may repeat)
	keys := make([]string, len(fields))
	seen := map[string]int{}
	props := map[string]interface{}{}
	var listing strings.Builder
	for i, f := range fields {
		k := strings.TrimSuffix(strings.TrimSpace(f.Label), ":")
		seen[k]++
		if seen[k] > 1 {
			k += " (" + strconv.Itoa(seen[k]) + ")"
		}
		keys[i] = k
		if f.Kind == "table" {
			cp := map[string]interface{}{}
			for _, c := range f.Cols {
				cp[c] = map[string]string{"type": "string"}
			}
			props[k] = map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object", "properties": cp}}
			fmt.Fprintf(&listing, "- %s : جدول، الأعمدة: %s\n", k, strings.Join(f.Cols, " | "))
		} else {
			props[k] = map[string]string{"type": "string"}
			fmt.Fprintf(&listing, "- %s\n", k)
		}
	}
	schema := map[string]interface{}{"type": "object", "properties": props}
	sys := "You fill a form from the user's notes. Reply ONLY with a JSON object whose keys are exactly the form field names. Copy values exactly as written in the notes (names, numbers, dates, codes). If a value is not in the notes use an empty string (or an empty array for tables). Never invent data. Keep the language of the notes. المطلوب: املأ النموذج من الملاحظات، ولا تخترع معلومات."
	user := "حقول النموذج:\n" + listing.String() + "\nالملاحظات:\n" + truncRunes(in.Text, 3500)
	call := func(withSchema bool) (string, int, error) {
		body := map[string]interface{}{
			"model": "local", "stream": false, "temperature": 0.1, "max_tokens": 1400,
			"messages": []map[string]string{{"role": "system", "content": sys}, {"role": "user", "content": user}},
		}
		if noThink {
			body["chat_template_kwargs"] = map[string]interface{}{"enable_thinking": false}
		}
		if withSchema {
			body["response_format"] = map[string]interface{}{"type": "json_schema", "json_schema": map[string]interface{}{"name": "form", "schema": schema}}
		}
		bb, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(r.Context(), "POST", "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/chat/completions", bytes.NewReader(bb))
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 20 * time.Minute}).Do(req)
		if err != nil {
			return "", 0, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if resp.StatusCode != 200 {
			return "", resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		var o struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(b, &o)
		if len(o.Choices) == 0 {
			return "", 200, fmt.Errorf("empty reply")
		}
		return o.Choices[0].Message.Content, 200, nil
	}
	txt, code, err := call(true)
	if err != nil && code == 400 {
		txt, _, err = call(false)
	}
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	var tf thinkFilter
	txt = tf.feed(txt) + tf.flush()
	js := extractJSONObject(txt)
	var got map[string]interface{}
	if json.Unmarshal([]byte(js), &got) != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "لم يُرجع النموذج بياناً مفهوماً — أعد المحاولة بنص أوضح"})
		return
	}
	values := map[string]interface{}{}
	for i, f := range fields {
		v, ok := got[keys[i]]
		if !ok {
			continue
		}
		if f.Kind == "table" {
			arr, _ := v.([]interface{})
			var rows []interface{}
			for _, it := range arr {
				m, _ := it.(map[string]interface{})
				var row []interface{}
				for _, c := range f.Cols {
					s := ""
					switch t := m[c].(type) {
					case string:
						s = t
					case float64:
						s = strconv.FormatFloat(t, 'f', -1, 64)
					}
					row = append(row, s)
				}
				rows = append(rows, row)
			}
			values[f.ID] = rows
		} else {
			switch t := v.(type) {
			case string:
				values[f.ID] = t
			case float64:
				values[f.ID] = strconv.FormatFloat(t, 'f', -1, 64)
			}
		}
	}
	writeJSON(w, map[string]interface{}{"ok": true, "values": values})
}

func extractJSONObject(s string) string {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return ""
	}
	return s[i : j+1]
}
