package main

import (
	"archive/zip"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/ocr
var ocrFS embed.FS

go:embed index.html
var indexHTML []byte

//go:embed hse.html
var hseFS embed.FS

//go:embed usbai.ico
var iconICO []byte

// SHA-256 of the password ("Hamid"); the plain text is not stored in the binary.
const passHash = "e8dc7c05827f477d61345fc5fbaf6dffe9a3bd213c682ea25522c1c290fc4f9e"

const (
	uiPortPreferred = 47821
	llamaPortStart  = 8080
	ctxSize         = 3072
	maxLoadSeconds  = 600
	watchdogSeconds = 150
)

type modelInfo struct {
	Name string  `json:"name"`
	Path string  `json:"-"`
	GB   float64 `json:"gb"`
	Fit  string  `json:"fit"`
	Arch string  `json:"arch"`
	Meta string  `json:"meta"`
}

type app struct {
	mu       sync.Mutex
	consent  bool // the user approved starting the model (asked in the UI)
	rootDir  string
	coreDir  string
	exe      string
	models   []modelInfo
	tokens   map[string]bool
	state    string // idle | loading | running | error
	model    string
	port     int
	cmd      *exec.Cmd
	logPath  string
	errMsg   string
	started  time.Time
	lastPing time.Time
	byeTimer *time.Timer
	uiURL    string
	opened   bool
	note     string
	noThink  bool
	rag      *ragService
	cat      *ragService
	kbMu     sync.Mutex
	kbIdx    *searchIndex
	kbList   []kbEntry
	kbKey    int
	hub      *hubStore
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *app) logf(format string, args ...interface{}) {
	dir := filepath.Join(a.coreDir, "logs")
	if a.coreDir == "" || os.MkdirAll(dir, 0o755) != nil {
		dir = os.TempDir()
	}
	f, err := os.OpenFile(filepath.Join(dir, "usbai.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// ---------- locating AI_CORE ----------
func findCore(root string) string {
	cands := []string{}
	if vol := filepath.VolumeName(root); vol != "" {
		cands = append(cands, vol+string(os.PathSeparator)+"AI_CORE")
	}
	cands = append(cands, filepath.Join(root, "AI_CORE"))
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

var exeRe = regexp.MustCompile(`(?i)^llamafile.*\.exe$`)

func (a *app) scan() {
	var models []modelInfo
	var exes []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			full := filepath.Join(dir, e.Name())
			low := strings.ToLower(e.Name())
			if e.IsDir() {
				if depth < 3 && low != "logs" {
					walk(full, depth+1)
				}
				continue
			}
			if strings.HasSuffix(low, ".gguf") && !strings.HasPrefix(low, "mmproj") {
				if info, err := e.Info(); err == nil {
					mi := modelInfo{Name: e.Name(), Path: full, GB: float64(info.Size()) / (1 << 30)}
					if arch, gname, gerr := readGGUFInfo(full); gerr == nil {
						mi.Arch, mi.Meta = arch, gname
					} else {
						mi.Meta = "(" + gerr.Error() + ")"
					}
					models = append(models, mi)
				}
			} else if exeRe.MatchString(e.Name()) {
				exes = append(exes, full)
			}
		}
	}
	if a.coreDir != "" {
		walk(a.coreDir, 0)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].GB < models[j].GB })
	sort.Slice(exes, func(i, j int) bool { return versionLess(filepath.Base(exes[i]), filepath.Base(exes[j])) })
	a.models = models
	a.exe = ""
	if len(exes) > 0 {
		a.exe = exes[len(exes)-1]
	}
}

func fitLabel(gb, free float64) string {
	if free <= 0 {
		return "unknown"
	}
	need := gb*1.25 + 0.4
	switch {
	case need <= free:
		return "ok"
	case need <= free*1.2:
		return "tight"
	}
	return "heavy"
}

// ---------- ports ----------
func portFree(p int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}
func freePort(start int) int {
	for p := start; p < start+200; p++ {
		if portFree(p) {
			return p
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return start
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func ping(port int) bool {
	c := http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == 404 {
		r2, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
		if err != nil {
			return false
		}
		defer r2.Body.Close()
		return r2.StatusCode == 200
	}
	return resp.StatusCode == 200
}

func tailLines(path string, n int) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// ---------- model process ----------
type launchOpts struct {
	noBrowser string // "--nobrowser" or "" when the build rejects it
	ctxFlag   string // "--ctx-size" or "-c"
	noMmap    bool   // --no-mmap (read the model into RAM instead of memory-mapping the drive)
	cpuOnly   bool   // -ngl 0
	noThink   bool   // --reasoning-budget 0 (disable Qwen3 "thinking": much faster answers)
	drop      string // optional flags this llamafile build rejected (space separated)
}

func (o launchOpts) dropped(flag string) bool {
	return strings.Contains(" "+o.drop+" ", " "+flag+" ")
}

func (o launchOpts) args(m modelInfo, port, threads int) []string {
	args := []string{"--server", "--model", m.Path, "--host", "127.0.0.1",
		"--port", strconv.Itoa(port), o.ctxFlag, strconv.Itoa(ctxSize), "-t", strconv.Itoa(threads)}
	if o.noBrowser != "" {
		args = append(args, o.noBrowser)
	}
	if o.noMmap {
		args = append(args, "--no-mmap")
	}
	if o.cpuOnly {
		args = append(args, "-ngl", "0")
	}
	// memory: one conversation slot instead of 4 (saves RAM, no speed cost for a single user)
	if !o.dropped("-np") {
		args = append(args, "-np", "1")
	}
	// prompt processing is compute-bound: use all logical cores for it
	if logical := runtime.NumCPU(); logical > threads && !o.dropped("-tb") {
		if logical > 8 {
			logical = 8
		}
		args = append(args, "-tb", strconv.Itoa(logical))
	}
	if o.noThink && !o.dropped("--reasoning-budget") {
		args = append(args, "--reasoning-budget", "0")
	}
	return args
}

var invalidArgRe = regexp.MustCompile(`invalid argument:\s*(\S+)`)

func (a *app) startModel(id int, noThink bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == "loading" || a.state == "running" {
		return fmt.Errorf("a model is already running")
	}
	if len(a.models) == 0 {
		a.scan()
	}
	if id < 0 || id >= len(a.models) {
		return fmt.Errorf("invalid model")
	}
	if a.exe == "" {
		return fmt.Errorf("llamafile*.exe not found in AI_CORE")
	}
	m := a.models[id]
	port := freePort(llamaPortStart)
	threads := runtime.NumCPU()
	if threads > 2 {
		threads /= 2
	}
	if threads > 6 {
		threads = 6
	}
	a.model, a.port, a.noThink = m.Name, port, noThink
	a.consent = true
	a.state, a.errMsg, a.started, a.opened, a.note = "loading", "", time.Now(), false, ""
	a.logf("exe=%s  model=%s  arch=%q  gguf-name=%q", a.exe, m.Path, m.Arch, m.Meta)
	if err := a.spawnLocked(m, port, threads, launchOpts{noBrowser: "--nobrowser", ctxFlag: "--ctx-size", cpuOnly: true, noThink: noThink}, 0); err != nil {
		a.state = "idle"
		return err
	}
	return nil
}

// spawnLocked starts llamafile (a.mu must be held). If the build rejects an optional
// flag ("invalid argument: X") the process is relaunched once without/with the alternative.
func (a *app) spawnLocked(m modelInfo, port, threads int, o launchOpts, attempt int) error {
	logDir := filepath.Join(a.coreDir, "logs")
	if os.MkdirAll(logDir, 0o755) != nil {
		logDir = os.TempDir()
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("server_%d.log", port))
	lf, err := os.Create(logPath)
	if err != nil {
		logPath = filepath.Join(os.TempDir(), fmt.Sprintf("usbai_server_%d.log", port))
		if lf, err = os.Create(logPath); err != nil {
			return err
		}
	}
	args := o.args(m, port, threads)
	cmd := exec.Command(a.exe, args...)
	cmd.Dir = filepath.Dir(a.exe)
	cmd.Stdout, cmd.Stderr = lf, lf
	hideCmd(cmd)
	if err := cmd.Start(); err != nil {
		lf.Close()
		return err
	}
	a.cmd, a.logPath = cmd, logPath
	a.logf("start model=%s port=%d threads=%d pid=%d attempt=%d args=%q", m.Name, port, threads, cmd.Process.Pid, attempt, args)
	go func(c *exec.Cmd) {
		werr := c.Wait()
		lf.Close()
		a.mu.Lock()
		defer a.mu.Unlock()
		a.logf("process pid=%d exited: %v (after %.0fs, state=%s)", c.Process.Pid, werr, time.Since(a.started).Seconds(), a.state)
		if a.cmd != c {
			return
		}
		a.cmd = nil
		if a.state != "loading" && a.state != "running" {
			return
		}
		if a.state == "loading" && attempt < 5 {
			tail := strings.Join(tailLines(logPath, 8), "\n")
			n := o
			why := ""
			if mm := invalidArgRe.FindStringSubmatch(tail); mm != nil {
				switch mm[1] {
				case "--nobrowser":
					n.noBrowser, why = "", "flag --nobrowser rejected"
				case "--ctx-size":
					n.ctxFlag, why = "-c", "flag --ctx-size rejected"
				case "--no-mmap":
					n.noMmap, why = false, "flag --no-mmap rejected"
				case "-ngl":
					n.cpuOnly, why = false, "flag -ngl rejected"
				case "-np", "-tb", "--reasoning-budget":
					n.drop, why = strings.TrimSpace(o.drop+" "+mm[1]), "flag "+mm[1]+" rejected"
				}
			} else if !o.noMmap {
				n.noMmap, why = true, "crash while loading: retrying with --no-mmap"
			} else if !o.cpuOnly {
				n.cpuOnly, why = true, "crash again: retrying CPU-only (-ngl 0)"
			}
			if why != "" && n != o {
				a.logf("%s", why)
				a.note = why
				a.cmd = nil
				if err := a.spawnLocked(m, port, threads, n, attempt+1); err == nil {
					return
				}
			}
		}
		a.state = "error"
		a.errMsg = fmt.Sprintf("process exited unexpectedly (%v)", werr)
	}(cmd)
	go a.waitReady(cmd)
	return nil
}

func (a *app) waitReady(c *exec.Cmd) {
	for {
		time.Sleep(time.Second)
		a.mu.Lock()
		if a.cmd != c || a.state != "loading" {
			a.mu.Unlock()
			return
		}
		el := time.Since(a.started)
		port := a.port
		a.mu.Unlock()
		if ping(port) {
			a.mu.Lock()
			if a.cmd == c && a.state == "loading" {
				a.state = "running"
				a.logf("server ready on port %d after %.0fs", port, time.Since(a.started).Seconds())
			}
			a.opened = true
			a.mu.Unlock()
			return
		}
		if el > maxLoadSeconds*time.Second {
			a.stopModel()
			a.mu.Lock()
			a.state, a.errMsg = "error", fmt.Sprintf("timeout after %d s (slow drive or not enough memory)", maxLoadSeconds)
			a.mu.Unlock()
			return
		}
	}
}

func (a *app) stopModel() {
	a.mu.Lock()
	a.consent = false
	c := a.cmd
	a.cmd = nil
	if a.state == "loading" || a.state == "running" {
		a.state = "idle"
	}
	a.mu.Unlock()
	if c != nil && c.Process != nil {
		killTree(c.Process.Pid)
		_ = c.Process.Kill()
	}
}

func (a *app) shutdown(why string) {
	a.logf("shutdown: %s", why)
	a.stopModel()
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
}

// ---------- HTTP ----------
func (a *app) authed(r *http.Request) bool {
	c, err := r.Cookie("ua")
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokens[c.Value]
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/hse", func(w http.ResponseWriter, r *http.Request) {
		if !a.authed(r) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		b, _ := hseFS.ReadFile("web/hse.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/hse/", func(w http.ResponseWriter, r *http.Request) {
		n := strings.TrimPrefix(r.URL.Path, "/hse/")
		b, err := hseFS.ReadFile("web/hse/" + n)
		if err != nil || strings.Contains(n, "/") {
			http.NotFound(w, r)
			return
		}
		ct := "text/plain"
		if strings.HasSuffix(n, ".js") {
			ct = "application/javascript; charset=utf-8"
		} else if strings.HasSuffix(n, ".css") {
			ct = "text/css; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		_, _ = w.Write(iconICO)
	})
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("usbai")) })
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Password string `json:"password"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
		sum := sha256.Sum256([]byte(in.Password))
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(passHash)) != 1 {
			time.Sleep(400 * time.Millisecond)
			writeJSON(w, map[string]interface{}{"ok": false})
			return
		}
		t := newToken()
		a.mu.Lock()
		a.tokens[t] = true
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "ua", Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		writeJSON(w, map[string]interface{}{"ok": true})
	})
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.lastPing = time.Now()
		if a.byeTimer != nil {
			a.byeTimer.Stop()
			a.byeTimer = nil
		}
		a.mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/bye", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		if a.byeTimer != nil {
			a.byeTimer.Stop()
		}
		a.byeTimer = time.AfterFunc(8*time.Second, func() { a.shutdown("control page closed") })
		a.mu.Unlock()
	})
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !a.authed(r) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/models", guard(func(w http.ResponseWriter, r *http.Request) {
		a.scan()
		free := freeRAMGB()
		for i := range a.models {
			a.models[i].Fit = fitLabel(a.models[i].GB, free)
		}
		writeJSON(w, map[string]interface{}{
			"core": a.coreDir, "exe": a.exe != "", "free_gb": free, "models": a.models,
		})
	}))
	mux.HandleFunc("/api/start", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID      int  `json:"id"`
			NoThink bool `json:"noThink"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
		if err := a.startModel(in.ID, in.NoThink); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	mux.HandleFunc("/api/status", guard(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		st, model, port, em, lp, started, note := a.state, a.model, a.port, a.errMsg, a.logPath, a.started, a.note
		a.mu.Unlock()
		el := 0
		if st == "loading" || st == "running" {
			el = int(time.Since(started).Seconds())
		}
		n := 1
		if st == "error" {
			n = 14
		}
		writeJSON(w, map[string]interface{}{"state": st, "model": model, "port": port, "error": em, "elapsed": el, "note": note, "log": tailLines(lp, n), "logPath": lp})
	}))
	mux.HandleFunc("/api/stop", guard(func(w http.ResponseWriter, r *http.Request) {
		a.stopModel()
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	mux.HandleFunc("/api/open", guard(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		p := a.port
		run := a.state == "running"
		a.mu.Unlock()
		if run {
			openURL("http://127.0.0.1:" + strconv.Itoa(p))
		}
		writeJSON(w, map[string]interface{}{"ok": run})
	}))
	mux.HandleFunc("/api/chat", guard(a.handleChat))
	mux.HandleFunc("/api/rag/status", guard(func(w http.ResponseWriter, r *http.Request) { 
		st := a.rag.status()
		if a.cat != nil {
			cs := a.cat.status()
			for _, f := range cs.Files {
				f.Rel = catPrefix + f.Rel
				st.Files = append(st.Files, f)
			}
			st.Chunks += cs.Chunks
			st.Indexing = st.Indexing || cs.Indexing
		}
		writeJSON(w, st)
	}))
	mux.HandleFunc("/api/rag/reindex", guard(func(w http.ResponseWriter, r *http.Request) {
		a.rag.reindex()
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	mux.HandleFunc("/api/rag/search", guard(func(w http.ResponseWriter, r *http.Request) {
		si := a.rag.searcher()
		var out []map[string]interface{}
		if si != nil {
			for _, h := range si.search(r.URL.Query().Get("q"), 5) {
				c := si.data.Chunks[h.Chunk]
				out = append(out, map[string]interface{}{"file": si.data.Files[c.File].Rel, "page": c.Page, "score": h.Score, "coverage": h.Coverage, "text": c.Text})
			}
		}
		writeJSON(w, map[string]interface{}{"hits": out})
	}))
	mux.HandleFunc("/ocr/", func(w http.ResponseWriter, r *http.Request) {
		name := "web" + r.URL.Path
		b, err := ocrFS.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ct := "application/octet-stream"
		switch {
		case strings.HasSuffix(name, ".js"), strings.HasSuffix(name, ".mjs"):
			ct = "text/javascript; charset=utf-8"
		case strings.HasSuffix(name, ".wasm"):
			ct = "application/wasm"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/rag/file", guard(func(w http.ResponseWriter, r *http.Request) {
		full, ok := a.rag.safeRel(r.URL.Query().Get("rel"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, full)
	}))
	mux.HandleFunc("/api/rag/media", guard(func(w http.ResponseWriter, r *http.Request) {
		full, ok := a.rag.safeRel(r.URL.Query().Get("rel"))
		n := r.URL.Query().Get("n")
		isChart := chartEntryRe.MatchString(n)
		if !ok || !(isChart || ((strings.HasPrefix(n, "word/media/") || strings.HasPrefix(n, "ppt/media/") || strings.HasPrefix(n, "xl/media/")) && imgExts[strings.ToLower(filepath.Ext(n))])) {
			http.NotFound(w, r)
			return
		}
		zr, err := zip.OpenReader(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer zr.Close()
		b, ok := zipFile(zr, n)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if isChart {
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "max-age=3600")
			_, _ = w.Write([]byte(parseChart(b).svg()))
			return
		}
		ct := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp"}[strings.ToLower(filepath.Ext(n))]
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "max-age=3600")
		_, _ = w.Write(b)
	}))
	mux.HandleFunc("/api/rag/ocr", guard(func(w http.ResponseWriter, r *http.Request) {
		rel := r.URL.Query().Get("rel")
		if _, ok := a.rag.safeRel(rel); !ok {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Pages []struct {
				Page int    `json:"page"`
				Text string `json:"text"`
			} `json:"pages"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&in); err != nil {
			http.Error(w, "bad json", 400)
			return
		}
		var pg []pageText
		for _, p := range in.Pages {
			pg = append(pg, pageText{Page: p.Page, Text: stripBidi.Replace(p.Text)})
		}
		if err := a.rag.saveOCR(filepath.ToSlash(rel), pg); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		a.rag.reindex()
		if a.cat != nil && strings.HasPrefix(filepath.ToSlash(rel), catPrefix) {
			a.cat.reindex()
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	}))
	a.hubHandlers(guard, mux)
	a.stopHandlers(guard, mux)
	a.pobHandlers(guard, mux)
	a.reportHandlers(guard, mux)
	a.opsHandlers(guard, mux)
	a.dailyHandlers(guard, mux)
	a.formHandlers(guard, mux)
	a.aiHandlers(guard, mux)
	a.inductionHandlers(guard, mux)
	a.archiveHandlers(guard, mux)
	a.pobImportHandlers(guard, mux)
	a.stopArchHandlers(guard, mux)
	a.profileHandlers(guard, mux)
	a.catalogHandlers(guard, mux)
	a.ocrTabHandlers(guard, mux)
	a.extraHandlers(guard, mux)
	a.kbHandlers(guard, mux)
	a.assistHandlers(guard, mux)
	a.stopDBHandlers(guard, mux)
	a.stopEvalHandler(guard, mux)
	a.stopSeedHandlers(guard, mux)
	a.chkHandlers(guard, mux)
	a.quickHandlers(guard, mux)
	mux.HandleFunc("/api/hub/pointage", guard(a.handlePointage))
	mux.HandleFunc("/api/hub/people/import-timesheet", guard(a.timesheetImportHandler))
	mux.HandleFunc("/api/export", guard(a.handleExport))
	mux.HandleFunc("/api/tpl/list", guard(a.handleTplList))
	mux.HandleFunc("/api/tpl/fields", guard(a.handleTplFields))
	mux.HandleFunc("/api/tpl/fill", guard(a.handleTplFill))
	mux.HandleFunc("/api/tpl/autofill", guard(a.handleTplAutofill))
	mux.HandleFunc("/api/quit", guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"ok": true})
		go a.shutdown("quit requested")
	}))
	return mux
}

func main() {
	exePath, err := os.Executable()
	if err != nil {
		msgBox("Usb Ai", "Cannot locate the program: "+err.Error())
		return
	}
	root := filepath.Dir(exePath)
	if d := os.Getenv("USBAI_ROOT"); d != "" { // used for testing only
		root = d
	}
	a := &app{rootDir: root, tokens: map[string]bool{}, state: "idle", lastPing: time.Now()}
	a.coreDir = findCore(root)
	ragDir := findRagDir(root)
	idxPath := filepath.Join(ragDir, ".index", "index.json")
	if a.coreDir != "" {
		idxPath = filepath.Join(a.coreDir, "cache", "rag_index.json")
	}
	a.rag = newRag(ragDir, idxPath, a.logf)
	a.hub = newHub(filepath.Join(filepath.Dir(ragDir), "data", "hse.json"))
	a.rag.reindex()
	go a.syncLoop()
	go func() { // STOP archive on the drive -> dictionary
		time.Sleep(3 * time.Second)
		a.scanStopArchive()
	}()

	// single instance: if our UI port already answers, just open it.
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(uiPortPreferred))
	if err != nil {
		c := http.Client{Timeout: 800 * time.Millisecond}
		if resp, e2 := c.Get("http://127.0.0.1:" + strconv.Itoa(uiPortPreferred) + "/api/hello"); e2 == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
			resp.Body.Close()
			if string(b) == "usbai" {
				openURL("http://127.0.0.1:" + strconv.Itoa(uiPortPreferred))
				return
			}
		}
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			msgBox("Usb Ai", "Cannot open a local port: "+err.Error())
			return
		}
	}
	a.uiURL = "http://" + ln.Addr().String()
	a.logf("ui at %s core=%q", a.uiURL, a.coreDir)

	srv := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	if os.Getenv("USBAI_NOBROWSER") == "" {
		openURL(a.uiURL)
	}

	// watchdog: browser page disappeared -> stop the model and exit
	for {
		time.Sleep(10 * time.Second)
		a.mu.Lock()
		gone := time.Since(a.lastPing) > watchdogSeconds*time.Second
		a.mu.Unlock()
		if gone {
			a.shutdown("no heartbeat from the browser page")
		}
	}
}

var numRe = regexp.MustCompile(`\d+`)

// versionLess compares names like llamafile-0.9.3.exe < llamafile-0.10.6.exe numerically.
func versionLess(x, y string) bool {
	a, b := numRe.FindAllString(x, -1), numRe.FindAllString(y, -1)
	for i := 0; i < len(a) && i < len(b); i++ {
		ai, _ := strconv.Atoi(a[i])
		bi, _ := strconv.Atoi(b[i])
		if ai != bi {
			return ai < bi
		}
	}
	return len(a) < len(b)
}

func findRagDir(root string) string {
	// the references folder may be called rag / rags / references... and sit next to the program, one level up, or at the drive root
	names := map[string]bool{"rag": true, "rags": true, "reference": true, "references": true, "refs": true, "مراجع": true, "المراجع": true}
	var bases []string
	if vol := filepath.VolumeName(root); vol != "" {
		bases = append(bases, vol+string(os.PathSeparator))
	}
	bases = append(bases, root, filepath.Dir(root))
	best, bestN := "", -1
	seen := map[string]bool{}
	for _, b := range bases {
		ents, err := os.ReadDir(b)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() || !names[strings.ToLower(e.Name())] {
				continue
			}
			p := filepath.Join(b, e.Name())
			if seen[p] {
				continue
			}
			seen[p] = true
			n := 0
			_ = filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && supported(d.Name()) && !strings.Contains(q, ".index") {
					n++
				}
				return nil
			})
			if n > bestN {
				best, bestN = p, n
			}
		}
	}
	if best != "" {
		return best
	}
	return filepath.Join(root, "rag")
}

// stripBidi removes invisible direction marks that OCR engines add to Arabic text.
var stripBidi = strings.NewReplacer("\u200e", "", "\u200f", "", "\u202a", "", "\u202b", "", "\u202c", "", "\u202d", "", "\u202e", "", "\u2066", "", "\u2067", "", "\u2068", "", "\u2069", "")
