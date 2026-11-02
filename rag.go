package main

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// ======================================================================
// Text extraction
// ======================================================================

type pageText struct {
	Page   int
	Text   string
	HasImg bool `json:",omitempty"`
}

func isArabicRune(r rune) bool {
	return (r >= 0x0600 && r <= 0x06FF) || (r >= 0x0750 && r <= 0x077F) || (r >= 0xFB50 && r <= 0xFDFF) || (r >= 0xFE70 && r <= 0xFEFF)
}

func arabicCount(s string) int {
	n := 0
	for _, r := range s {
		if isArabicRune(r) && unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// expandForms converts Arabic presentation forms to base letters and drops U+FFFD.
func expandForms(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == 0xFFFD {
			continue
		}
		if m, ok := arabicForms[r]; ok {
			b.WriteString(m)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var mirror = map[rune]rune{'(': ')', ')': '(', '[': ']', ']': '[', '{': '}', '}': '{', '<': '>', '>': '<', '«': '»', '»': '«'}

func isLTRRun(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= 0xC0 && r <= 0x24F)
}

// reverseVisual turns one line extracted in visual (left-to-right) order into
// logical order: reverse everything, then restore the digit/Latin runs.
func reverseVisual(line string) string {
	rs := []rune(line)
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
	for i := range rs {
		if m, ok := mirror[rs[i]]; ok {
			rs[i] = m
		}
	}
	// restore LTR runs (digits/Latin, with inner . , : / % -)
	i := 0
	for i < len(rs) {
		if !isLTRRun(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) {
			if isLTRRun(rs[j]) {
				j++
			} else if j+1 < len(rs) && isLTRRun(rs[j+1]) && strings.ContainsRune(".,:/%-", rs[j]) && j > i {
				j++
			} else {
				break
			}
		}
		for a, b := i, j-1; a < b; a, b = a+1, b-1 {
			rs[a], rs[b] = rs[b], rs[a]
		}
		i = j
	}
	return string(rs)
}

var commonAr = []string{"في", "من", "على", "إلى", "الى", "عن", "أن", "ان", "أو", "او", "هذا", "هذه", "التي", "الذي", "يجب", "عند", "كل", "مع", "بعد", "قبل", "أي", "لا", "ما", "كما", "إذا", "ذلك", "تم", "كان"}

func reverseStr(s string) string {
	rs := []rune(s)
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
	return string(rs)
}

// visualScore returns (forwardHits, reversedHits) of very common Arabic words.
func visualScore(lines []string) (int, int) {
	fwd, rev := 0, 0
	set := map[string]bool{}
	rset := map[string]bool{}
	for _, w := range commonAr {
		set[w] = true
		rset[reverseStr(w)] = true
	}
	for _, l := range lines {
		for _, w := range strings.FieldsFunc(l, func(r rune) bool { return !unicode.IsLetter(r) && !(r >= 0x064B && r <= 0x065F) }) {
			if set[w] {
				fwd++
			}
			if rset[w] {
				rev++
			}
		}
	}
	return fwd, rev
}

// assembleLines rebuilds text lines from positioned glyphs (needed for Arabic pages:
// the library's plain-text mode loses word gaps in some fonts and returns visual order).
func assembleLines(txt []pdf.Text) []string {
	type g struct {
		s       string
		x, y, w float64
		fs      float64
	}
	// Lam-alef ligatures arrive as ONE glyph whose text is "لا"/"لأ"/"لإ"/"لآ" (logical order)
	// inside a visual-order stream; the later whole-line reversal would flip it to "ال".
	// So store such glyphs pre-swapped. Fallback for fonts where the glyph was split in two:
	// a ligature glyph is far wider than a plain lam (>0.45em).
	txt = append([]pdf.Text(nil), txt...)
	for i := 0; i < len(txt); i++ {
		s := expandForms(txt[i].S)
		switch s {
		case "لا", "لأ", "لإ", "لآ":
			rs := []rune(s)
			txt[i].S = string([]rune{rs[1], rs[0]})
			continue
		}
		if i+1 < len(txt) && txt[i].S == "ل" && txt[i].FontSize > 0 && txt[i].W > 0.45*txt[i].FontSize {
			switch txt[i+1].S {
			case "ا", "أ", "إ", "آ":
				txt[i].S, txt[i+1].S = txt[i+1].S, txt[i].S
				i++
			}
		}
	}
	var gs []g
	for _, t := range txt {
		s := strings.ReplaceAll(t.S, "\uFFFD", "")
		if s == "" {
			continue
		}
		gs = append(gs, g{expandForms(s), t.X, t.Y, t.W, math.Max(t.FontSize, 1)})
	}
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].y > gs[j].y })
	var lines []string
	for i := 0; i < len(gs); {
		j := i
		for j < len(gs) && math.Abs(gs[j].y-gs[i].y) <= 0.4*gs[i].fs {
			j++
		}
		row := append([]g(nil), gs[i:j]...)
		sort.SliceStable(row, func(a, b int) bool { return row[a].x < row[b].x })
		var sb strings.Builder
		for k, e := range row {
			if k > 0 {
				p := row[k-1]
				gap := e.x - (p.x + p.w)
				if p.w > 0 && gap > 0.18*e.fs && !strings.HasSuffix(sb.String(), " ") && e.s != " " {
					sb.WriteString(" ")
				}
			}
			sb.WriteString(e.s)
		}
		l := strings.TrimSpace(sb.String())
		letters := 0
		for _, r := range l {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				letters++
			}
		}
		if letters > 0 || len(l) > 3 {
			lines = append(lines, l)
		}
		i = j
	}
	return lines
}

func extractPDF(path string) (pages []pageText, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("PDF parser crashed: %v", r)
		}
	}()
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw := make([][]string, 0, r.NumPage())
	hasImg := make([]bool, r.NumPage()+1)
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		hasImg[i] = pdfPageHasImage(p)
		var lines []string
		func() {
			defer func() { _ = recover() }()
			t, e := p.GetPlainText(nil)
			if e != nil && t == "" {
				return
			}
			t = expandForms(strings.ReplaceAll(t, "\r", ""))
			if arabicCount(t) > 0 {
				// Arabic page: rebuild lines from glyph positions
				if ls := assembleLines(p.Content().Text); len(ls) > 0 {
					lines = ls
					return
				}
			}
			for _, l := range strings.Split(t, "\n") {
				lines = append(lines, strings.TrimSpace(l))
			}
		}()
		raw = append(raw, lines)
	}
	// decide once per document whether Arabic lines are in visual order
	var all []string
	for _, ls := range raw {
		all = append(all, ls...)
	}
	fwd, rev := visualScore(all)
	reverse := rev >= fwd // default (no evidence): this extractor returns visual order
	for i, ls := range raw {
		var out []string
		for _, l := range ls {
			if reverse && arabicCount(l) > 0 && arabicCount(l)*2 >= len([]rune(strings.Join(strings.Fields(l), ""))) {
				l = reverseVisual(l)
			}
			out = append(out, l)
		}
		pages = append(pages, pageText{Page: i + 1, Text: strings.Join(out, "\n"), HasImg: hasImg[i+1]})
	}
	return pages, nil
}

func extractDOCX(path string) ([]pageText, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		rels := relTargets(zr, "word/_rels/document.xml.rels", "word")
		dec := xml.NewDecoder(rc)
		var b strings.Builder
		inT := false
		tbl := 0
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "t":
					inT = true
				case "tbl":
					tbl++
				case "blip", "imagedata":
					for _, at := range t.Attr {
						if at.Name.Local == "embed" || at.Name.Local == "id" {
							if tg, ok := rels[at.Value]; ok && okImage(zr, tg) {
								b.WriteString(" " + imgMark(tg) + " ")
							}
						}
					}
				case "tab":
					b.WriteString(" ")
				case "br", "cr":
					b.WriteString("\n")
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inT = false
				case "p":
					if tbl > 0 {
						b.WriteString(" ")
					} else {
						b.WriteString("\n\n")
					}
				case "tc":
					b.WriteString(" | ")
				case "tr":
					b.WriteString("\n\n")
				case "tbl":
					tbl--
				}
			case xml.CharData:
				if inT {
					b.Write(t)
				}
			}
		}
		return []pageText{{Page: 0, Text: b.String()}}, nil
	}
	return nil, fmt.Errorf("word/document.xml not found")
}

func extractText(path string) ([]pageText, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return extractPDF(path)
	case ".docx":
		return extractDOCX(path)
	case ".pptx":
		return extractPPTX(path)
	case ".xlsx", ".xlsm":
		return extractXLSX(path)
	case ".csv":
		return extractCSV(path)
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return extractImageFile(path)
	case ".txt", ".md":
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return []pageText{{Page: 0, Text: strings.TrimPrefix(string(b), "\xef\xbb\xbf")}}, nil
	}
	return nil, fmt.Errorf("unsupported type")
}

func supported(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".docx", ".pptx", ".xlsx", ".xlsm", ".csv", ".txt", ".md", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "~$")
	}
	return false
}

// ======================================================================
// Chunking
// ======================================================================

const (
	chunkTarget = 700
	chunkMax    = 1000
	chunkMin    = 160
)

type chunk struct {
	File   int      `json:"f"`
	Page   int      `json:"p"`
	Text   string   `json:"t"`
	Img    []string `json:"i,omitempty"` // images attached to this passage (zip entry names, or "@self")
	PdfImg bool     `json:"g,omitempty"` // the PDF page contains pictures/figures
}

var imgMarker = regexp.MustCompile("\x01IMG:([^\x01]*)\x01")

func imgMark(name string) string {
	return "\x01IMG:" + strings.ReplaceAll(name, ".", "\x02") + "\x01"
}

func splitSentences(s string) []string {
	var out []string
	var cur []rune
	for _, r := range s {
		cur = append(cur, r)
		if r == '.' || r == '؟' || r == '?' || r == '!' || r == '؛' || r == '\n' || r == '。' || r == '！' || r == '？' || r == '；' {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

func chunkText(text string, file, page int) []chunk {
	// paragraphs separated by blank lines; single newlines are hard wraps
	var res []chunk
	var paras []string
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n\n") {
		p = strings.Join(strings.Fields(strings.ReplaceAll(p, "\n", " ")), " ")
		if p != "" {
			paras = append(paras, p)
		}
	}
	var cur strings.Builder
	flush := func() {
		t := strings.TrimSpace(cur.String())
		var imgs []string
		for _, m := range imgMarker.FindAllStringSubmatch(t, -1) {
			imgs = append(imgs, strings.ReplaceAll(m[1], "\x02", "."))
		}
		t = strings.TrimSpace(strings.Join(strings.Fields(imgMarker.ReplaceAllString(t, " ")), " "))
		if t != "" {
			res = append(res, chunk{File: file, Page: page, Text: t, Img: imgs})
		}
		cur.Reset()
	}
	for _, p := range paras {
		for _, s := range splitSentences(p) {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			for len([]rune(s)) > chunkMax { // very long run without punctuation
				rs := []rune(s)
				if cur.Len() > 0 {
					flush()
				}
				cur.WriteString(string(rs[:chunkTarget]))
				flush()
				s = string(rs[chunkTarget:])
			}
			if len([]rune(cur.String()))+len([]rune(s)) > chunkTarget && len([]rune(cur.String())) >= chunkMin {
				flush()
			}
			if cur.Len() > 0 {
				cur.WriteString(" ")
			}
			cur.WriteString(s)
		}
	}
	flush()
	return res
}

// ======================================================================
// Normalisation / tokenisation (Arabic, French, English)
// ======================================================================

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
في من علي الي عن ان او ما لا ماذا كيف لماذا متي اين هل هي هو افعل اريد اعرف يمكن يمكنني ممكن ارجو اذكر اشرح هذا هذه ذلك تلك التي الذي الذين هو هي هم كما اذا قد كان تم يتم عند بعد قبل كل مع او ثم حتي اي بين لم لن ليس انه انها
the of and to a an in is are for on with as by at or be this that these those it its from was were been has have had not can may must shall will should would which who whom when where what how
le la les de des du et en un une pour dans que qui sur par au aux ce ces est sont avec ou ne pas se sa son ses leur leurs il elle ils elles plus ete etre`) {
		stop[w] = true
	}
}

var frAccents = strings.NewReplacer("é", "e", "è", "e", "ê", "e", "ë", "e", "à", "a", "â", "a", "ù", "u", "û", "u", "ü", "u", "î", "i", "ï", "i", "ô", "o", "ç", "c", "œ", "oe")

func normChar(r rune) (rune, bool) {
	switch {
	case r >= 0x064B && r <= 0x065F, r == 0x0670, r == 0x0640:
		return 0, false // diacritics, dagger alef, tatweel
	case r == 'أ' || r == 'إ' || r == 'آ' || r == 'ٱ':
		return 'ا', true
	case r == 'ى':
		return 'ي', true
	case r == 'ة':
		return 'ه', true
	case r == 'ؤ':
		return 'و', true
	case r == 'ئ':
		return 'ي', true
	}
	return r, true
}

var arPrefixes = []string{"وال", "فال", "بال", "كال", "لل", "ال"}

func stemAr(w string) string {
	// lam-alef ligature swap equivalence: PDF extraction may emit "ال" for "لا"
	w = strings.ReplaceAll(w, "لا", "ال")
	for _, p := range arPrefixes {
		if strings.HasPrefix(w, p) && len([]rune(w))-len([]rune(p)) >= 2 {
			w = strings.TrimPrefix(w, p)
			break
		}
	}
	rs := []rune(w)
	for _, suf := range []string{"ات", "ون", "ين", "ان"} {
		if len(rs) > 4 && strings.HasSuffix(w, suf) {
			w = strings.TrimSuffix(w, suf)
			rs = []rune(w)
			break
		}
	}
	if len(rs) > 3 && rs[len(rs)-1] == 'ه' {
		w = string(rs[:len(rs)-1])
	}
	return w
}

func stemLatin(w string) string {
	if len(w) > 5 && strings.HasSuffix(w, "es") {
		return w[:len(w)-2]
	}
	if len(w) > 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
		return w[:len(w)-1]
	}
	return w
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	s = frAccents.Replace(s)
	var toks []string
	var cur []rune
	arabic := false
	emit := func() {
		if len(cur) == 0 {
			return
		}
		w := string(cur)
		cur = cur[:0]
		if stop[w] {
			return
		}
		if arabic {
			w = stemAr(w)
		} else {
			w = stemLatin(w)
		}
		if len([]rune(w)) >= 2 && !stop[w] {
			toks = append(toks, w)
		}
	}
	var han []rune
	flushHan := func() {
		if len(han) == 1 {
			toks = append(toks, string(han))
		}
		for i := 0; i+1 < len(han); i++ {
			toks = append(toks, string(han[i:i+2]))
		}
		han = han[:0]
	}
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			emit()
			if strings.ContainsRune("的了吗呢和与及或", r) {
				flushHan()
			} else {
				han = append(han, r)
			}
			continue
		}
		if len(han) > 0 {
			flushHan()
		}
		if nr, ok := normChar(r); !ok {
			continue
		} else {
			r = nr
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			isAr := isArabicRune(r)
			if len(cur) > 0 && isAr != arabic {
				emit()
			}
			arabic = isAr
			cur = append(cur, r)
		} else {
			emit()
		}
	}
	emit()
	flushHan()
	return toks
}

// ======================================================================
// Index
// ======================================================================

type fileInfo struct {
	Rel    string `json:"rel"`
	Size   int64  `json:"size"`
	Mod    int64  `json:"mod"`
	Status string `json:"status"` // ok | empty | needs_ocr | error
	Msg    string `json:"msg"`
	Pages  int    `json:"pages"`
	Chunks int    `json:"chunks"`
	OCR    bool   `json:"ocr,omitempty"`
}

type indexData struct {
	Version int        `json:"version"`
	Files   []fileInfo `json:"files"`
	Chunks  []chunk    `json:"chunks"`
}

type posting struct {
	doc int32
	tf  int32
}

type searchIndex struct {
	data  indexData
	post  map[string][]posting
	dlen  []int32
	avgdl float64
	flat  []string
}

func buildSearch(d indexData) *searchIndex {
	si := &searchIndex{data: d, post: map[string][]posting{}, dlen: make([]int32, len(d.Chunks))}
	var total int64
	for i, c := range d.Chunks {
		toks := tokenize(c.Text)
		si.dlen[i] = int32(len(toks))
		total += int64(len(toks))
		tf := map[string]int32{}
		for _, t := range toks {
			tf[t]++
		}
		for t, n := range tf {
			si.post[t] = append(si.post[t], posting{int32(i), n})
		}
	}
	if len(d.Chunks) > 0 {
		si.avgdl = float64(total) / float64(len(d.Chunks))
	}
	return si
}

type hit struct {
	Chunk    int
	Score    float64
	Coverage float64
}

func normText(s string) string {
	s = frAccents.Replace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if nr, ok := normChar(r); ok && (unicode.IsLetter(nr) || unicode.IsDigit(nr)) {
			b.WriteRune(nr)
		}
	}
	return strings.ReplaceAll(b.String(), "لا", "ال")
}

func (si *searchIndex) search(q string, k int) []hit {
	if si == nil || len(si.data.Chunks) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var terms []string
	for _, t := range tokenize(q) {
		if !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return nil
	}
	if si.flat == nil {
		si.flat = make([]string, len(si.data.Chunks))
		for i, c := range si.data.Chunks {
			si.flat[i] = normText(c.Text)
		}
	}
	const k1, b = 1.5, 0.75
	N := float64(len(si.data.Chunks))
	scores := map[int32]float64{}
	matched := map[int32]int{}
	known := 0
	gl := glossary()
	for _, t := range terms {
		variants := []string{t}
		for _, a := range gl[t] {
			if !seen[a] {
				variants = append(variants, a)
			}
		}
		termDocs := map[int32]bool{}
		termKnown := false
		for vi, v := range variants {
			w := 1.0
			if vi > 0 {
				w = 0.85 // translated / synonym term
			}
			pl := si.post[v]
			if len(pl) == 0 {
				// words glued together by the PDF extractor: substring fallback
				if rt := []rune(v); vi == 0 && len(rt) >= 4 {
					idf := math.Log(1 + (N+0.5)/1.5)
					for i, f := range si.flat {
						if strings.Contains(f, v) {
							scores[int32(i)] += idf * 0.6
							termDocs[int32(i)] = true
							termKnown = true
						}
					}
				}
				continue
			}
			termKnown = true
			idf := math.Log(1 + (N-float64(len(pl))+0.5)/(float64(len(pl))+0.5))
			for _, p := range pl {
				tf := float64(p.tf)
				dl := float64(si.dlen[p.doc])
				scores[p.doc] += w * idf * tf * (k1 + 1) / (tf + k1*(1-b+b*dl/math.Max(si.avgdl, 1)))
				termDocs[p.doc] = true
			}
		}
		if termKnown {
			known++
		}
		for d := range termDocs {
			matched[d]++
		}
	}
	if known == 0 {
		return nil
	}
	var hits []hit
	for d, s := range scores {
		hits = append(hits, hit{Chunk: int(d), Score: s, Coverage: float64(matched[d]) / float64(len(terms))})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Chunk < hits[j].Chunk
	})
	var out []hit
	for _, h := range hits {
		if len(out) >= k {
			break
		}
		m := matched[int32(h.Chunk)]
		cov := h.Coverage
		covKnown := float64(m) / float64(known)
		ok := cov >= 0.34 || (covKnown >= 0.5 && m >= 2) || (m >= 1 && len(terms) <= 2)
		if !ok {
			continue
		}
		if len(out) > 0 && h.Score < out[0].Score*0.35 {
			continue
		}
		out = append(out, h)
	}
	return out
}

// ======================================================================
// OCR cache (text produced by the in-browser OCR for scanned PDFs)
// ======================================================================

func (r *ragService) ocrPath(rel string, size, mod int64) string {
	h := sha1.Sum([]byte(rel))
	return filepath.Join(filepath.Dir(r.idxPath), "ocr", fmt.Sprintf("%x_%d_%d.json", h[:8], size, mod))
}

func (r *ragService) loadOCR(rel string, size, mod int64) []pageText {
	b, err := os.ReadFile(r.ocrPath(rel, size, mod))
	if err != nil {
		return nil
	}
	var pg []pageText
	if json.Unmarshal(b, &pg) != nil {
		return nil
	}
	return pg
}

func (r *ragService) saveOCR(rel string, pg []pageText) error {
	if r.extra != nil && strings.HasPrefix(rel, catPrefix) {
		return r.extra.saveOCR(rel[len(catPrefix):], pg)
	}
	full := filepath.Join(r.dir, filepath.FromSlash(rel))
	st, err := os.Stat(full)
	if err != nil {
		return err
	}
	p := r.ocrPath(rel, st.Size(), st.ModTime().Unix())
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(pg)
	return os.WriteFile(p, b, 0o644)
}

const catPrefix = "Catalogs/"

// safeRel resolves a relative path inside the rag folder (no traversal).
func (r *ragService) safeRel(rel string) (string, bool) {
	rel = filepath.ToSlash(filepath.Clean("/" + rel))[1:]
	if r.extra != nil && strings.HasPrefix(rel, catPrefix) {
		return r.extra.safeRel(rel[len(catPrefix):])
	}
	if rel == "" || !supported(rel) {
		return "", false
	}
	return filepath.Join(r.dir, filepath.FromSlash(rel)), true
}

// needsOCR: a PDF whose text layer is (almost) empty.
func needsOCR(path string, pages []pageText) bool {
	if strings.ToLower(filepath.Ext(path)) != ".pdf" || len(pages) == 0 {
		return false
	}
	n := 0
	for _, p := range pages {
		n += len([]rune(strings.TrimSpace(p.Text)))
	}
	return n < 30*len(pages)
}

// ======================================================================
// Indexing service
// ======================================================================

type ragStatus struct {
	Dir      string     `json:"dir"`
	Exists   bool       `json:"exists"`
	Indexing bool       `json:"indexing"`
	Current  string     `json:"current"`
	Done     int        `json:"done"`
	Total    int        `json:"total"`
	Files    []fileInfo `json:"files"`
	Chunks   int        `json:"chunks"`
	Error    string     `json:"error"`
	Updated  string     `json:"updated"`
}

type ragService struct {
	mu      sync.Mutex
	dir     string
	idxPath string
	idx     *searchIndex
	extra   *ragService // Catalogs folder, addressed as "Catalogs/<rel>"
	st      ragStatus
	logf    func(string, ...interface{})
}

func newRag(dir, idxPath string, logf func(string, ...interface{})) *ragService {
	r := &ragService{dir: dir, idxPath: idxPath, logf: logf}
	r.st.Dir = dir
	if b, err := os.ReadFile(idxPath); err == nil {
		var d indexData
		if json.Unmarshal(b, &d) == nil && d.Version == 2 {
			r.idx = buildSearch(d)
			r.st.Files, r.st.Chunks = d.Files, len(d.Chunks)
		}
	}
	return r
}

func (r *ragService) status() ragStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.st
	if st, err := os.Stat(r.dir); err == nil && st.IsDir() {
		s.Exists = true
	}
	return s
}

func (r *ragService) searcher() *searchIndex {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.idx
}

func (r *ragService) reindex() {
	r.mu.Lock()
	if r.st.Indexing {
		r.mu.Unlock()
		return
	}
	r.st.Indexing, r.st.Error, r.st.Done, r.st.Total, r.st.Current = true, "", 0, 0, ""
	prev := r.idx
	r.mu.Unlock()
	go func() {
		err := r.run(prev)
		r.mu.Lock()
		r.st.Indexing, r.st.Current = false, ""
		if err != nil {
			r.st.Error = err.Error()
		}
		r.st.Updated = time.Now().Format("15:04:05")
		r.mu.Unlock()
	}()
}

func (r *ragService) run(prev *searchIndex) error {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	type fileEntry struct {
		rel, full string
		size, mod int64
	}
	var entries []fileEntry
	_ = filepath.Walk(r.dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && p != r.dir {
				return filepath.SkipDir
			}
			return nil
		}
		if supported(info.Name()) {
			rel, _ := filepath.Rel(r.dir, p)
			entries = append(entries, fileEntry{filepath.ToSlash(rel), p, info.Size(), info.ModTime().Unix()})
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	r.mu.Lock()
	r.st.Total = len(entries)
	r.mu.Unlock()

	// reuse unchanged files from the previous index
	oldChunks := map[string][]chunk{}
	oldInfo := map[string]fileInfo{}
	if prev != nil {
		for i, f := range prev.data.Files {
			oldInfo[f.Rel] = f
			_ = i
		}
		for _, c := range prev.data.Chunks {
			if c.File < len(prev.data.Files) {
				rel := prev.data.Files[c.File].Rel
				oldChunks[rel] = append(oldChunks[rel], c)
			}
		}
	}
	var nd indexData
	nd.Version = 2
	for i, e := range entries {
		r.mu.Lock()
		r.st.Current, r.st.Done = e.rel, i
		r.mu.Unlock()
		fi := fileInfo{Rel: e.rel, Size: e.size, Mod: e.mod}
		idx := len(nd.Files)
		if of, ok := oldInfo[e.rel]; ok && of.Size == e.size && of.Mod == e.mod && of.Status != "error" && of.Status != "needs_ocr" {
			fi = of
			nd.Files = append(nd.Files, fi)
			for _, c := range oldChunks[e.rel] {
				c.File = idx
				nd.Chunks = append(nd.Chunks, c)
			}
			continue
		}
		type res struct {
			pages []pageText
			err   error
		}
		ch := make(chan res, 1)
		go func(path string) {
			p, err := extractText(path)
			ch <- res{p, err}
		}(e.full)
		var rs res
		select {
		case rs = <-ch:
		case <-time.After(4 * time.Minute):
			rs = res{err: fmt.Errorf("timeout while reading the file")}
		}
		if rs.err == nil && needsOCR(e.full, rs.pages) {
			if pg := r.loadOCR(e.rel, e.size, e.mod); pg != nil {
				rs.pages = pg
				fi.OCR = true
			}
		}
		if rs.err != nil {
			fi.Status, fi.Msg = "error", rs.err.Error()
			nd.Files = append(nd.Files, fi)
			r.logf("rag: %s: error: %v", e.rel, rs.err)
			continue
		}
		fi.Pages = len(rs.pages)
		n0 := len(nd.Chunks)
		imgPages := 0
		for _, pg := range rs.pages {
			if pg.HasImg {
				imgPages++
			}
		}
		// a picture on almost every page is a logo/header, not content
		pageImgs := !(len(rs.pages) >= 5 && float64(imgPages) > 0.6*float64(len(rs.pages)))
		for _, pg := range rs.pages {
			cs := chunkText(pg.Text, idx, pg.Page)
			if pg.HasImg && pageImgs {
				for k := range cs {
					cs[k].PdfImg = true
				}
			}
			nd.Chunks = append(nd.Chunks, cs...)
		}
		dropRepeatedImages(nd.Chunks[n0:])
		fi.Chunks = len(nd.Chunks) - n0
		if fi.Chunks == 0 && needsOCR(e.full, rs.pages) {
			fi.Status, fi.Msg = "needs_ocr", "ملف ممسوح ضوئياً (صور) — شغّل OCR من زر «قراءة الملفات الممسوحة»"
		} else if fi.Chunks == 0 {
			fi.Status, fi.Msg = "empty", "لا يوجد نص قابل للاستخراج"
		} else {
			fi.Status = "ok"
		}
		nd.Files = append(nd.Files, fi)
		r.logf("rag: %s: %s pages=%d chunks=%d", e.rel, fi.Status, fi.Pages, fi.Chunks)
	}
	si := buildSearch(nd)
	if b, err := json.Marshal(nd); err == nil {
		_ = os.MkdirAll(filepath.Dir(r.idxPath), 0o755)
		tmp := r.idxPath + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, r.idxPath)
		}
	}
	r.mu.Lock()
	r.idx = si
	r.st.Files, r.st.Chunks, r.st.Done = nd.Files, len(nd.Chunks), len(entries)
	r.mu.Unlock()
	return nil
}
