package main

// Template analysis + filling for .docx and .xlsx (pure Go, text-level OOXML edits so the
// original formatting is preserved).

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type tplField struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Kind  string   `json:"kind"`           // field | table
	Cols  []string `json:"cols,omitempty"` // table headers
	Rows  int      `json:"rows,omitempty"` // suggested number of rows
	Where string   `json:"where,omitempty"`
}

// ------------------------------------------------------------ generic XML child scanner

type xnode struct {
	Tag        string
	Start, End int // [Start,End) of the whole element
	Inner0     int // start of inner content
	Inner1     int // end of inner content
}

func tagNameAt(s string, i int) (name string, closing bool, selfClose bool, end int) {
	// s[i] == '<'
	j := i + 1
	if j < len(s) && s[j] == '/' {
		closing = true
		j++
	}
	k := j
	for k < len(s) && s[k] != ' ' && s[k] != '>' && s[k] != '/' && s[k] != '\n' && s[k] != '\t' && s[k] != '\r' {
		k++
	}
	name = s[j:k]
	// find end of tag, respecting quotes
	q := byte(0)
	for e := k; e < len(s); e++ {
		c := s[e]
		if q != 0 {
			if c == q {
				q = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			q = c
			continue
		}
		if c == '>' {
			selfClose = e > 0 && s[e-1] == '/'
			return name, closing, selfClose, e + 1
		}
	}
	return name, closing, false, len(s)
}

// children returns the direct child elements of s[from:to].
func children(s string, from, to int) []xnode {
	var out []xnode
	depth := 0
	var cur xnode
	i := from
	for i < to {
		lt := strings.IndexByte(s[i:to], '<')
		if lt < 0 {
			break
		}
		i += lt
		if strings.HasPrefix(s[i:], "<!--") {
			e := strings.Index(s[i:], "-->")
			if e < 0 {
				break
			}
			i += e + 3
			continue
		}
		if strings.HasPrefix(s[i:], "<?") || strings.HasPrefix(s[i:], "<![CDATA[") {
			e := strings.IndexByte(s[i:], '>')
			if e < 0 {
				break
			}
			i += e + 1
			continue
		}
		name, closing, self, end := tagNameAt(s, i)
		switch {
		case closing:
			depth--
			if depth == 0 {
				cur.Inner1 = i
				cur.End = end
				out = append(out, cur)
			}
		case self:
			if depth == 0 {
				out = append(out, xnode{name, i, end, end, end})
			}
		default:
			if depth == 0 {
				cur = xnode{Tag: name, Start: i, Inner0: end}
			}
			depth++
		}
		i = end
	}
	return out
}

func childrenOf(s string, n xnode, tag string) []xnode {
	var out []xnode
	for _, c := range children(s, n.Inner0, n.Inner1) {
		if c.Tag == tag {
			out = append(out, c)
		}
	}
	return out
}

var wtRe = regexp.MustCompile(`(?s)<w:t(?: [^>]*)?>(.*?)</w:t>`)

func nodeText(s string, n xnode) string {
	var b strings.Builder
	for _, m := range wtRe.FindAllStringSubmatch(s[n.Start:n.End], -1) {
		b.WriteString(html.UnescapeString(m[1]))
	}
	return strings.TrimSpace(b.String())
}

// ------------------------------------------------------------ DOCX

var paraFieldRe = regexp.MustCompile(`^(.{2,45}?)\s*[:：]\s*[_.…\-–\s]*$`)

func docxBody(doc string) (xnode, bool) {
	for _, c := range children(doc, 0, len(doc)) {
		if c.Tag == "w:document" {
			for _, b := range childrenOf(doc, c, "w:body") {
				return b, true
			}
		}
	}
	return xnode{}, false
}

func analyzeDOCX(path string) ([]tplField, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	b, ok := zipFile(zr, "word/document.xml")
	if !ok {
		return nil, fmt.Errorf("word/document.xml not found")
	}
	doc := string(b)
	body, ok := docxBody(doc)
	if !ok {
		return nil, fmt.Errorf("bad document")
	}
	var fields []tplField
	ti, pi := 0, 0
	for _, ch := range children(doc, body.Inner0, body.Inner1) {
		switch ch.Tag {
		case "w:tbl":
			rows := childrenOf(doc, ch, "w:tr")
			grid := make([][]string, len(rows))
			for ri, r := range rows {
				for _, c := range childrenOf(doc, r, "w:tc") {
					grid[ri] = append(grid[ri], nodeText(doc, c))
				}
			}
			// header + empty rows => list table
			isList := false
			if len(grid) >= 2 {
				head := 0
				for _, c := range grid[0] {
					if c != "" {
						head++
					}
				}
				empty := 0
				for _, r := range grid[1:] {
					all := true
					for _, c := range r {
						if c != "" {
							all = false
						}
					}
					if all {
						empty++
					}
				}
				isList = head >= 2 && empty >= 1 && empty >= (len(grid)-1)/2 && len(grid[0]) >= 2
			}
			if isList {
				var cols []string
				for _, c := range grid[0] {
					cols = append(cols, c)
				}
				fields = append(fields, tplField{ID: fmt.Sprintf("tt%d", ti), Label: firstNonEmpty(cols), Kind: "table", Cols: cols, Rows: len(grid) - 1, Where: fmt.Sprintf("جدول %d", ti+1)})
			} else {
				for ri, r := range grid {
					for ci := 0; ci+1 < len(r); ci++ {
						if r[ci] != "" && r[ci+1] == "" {
							fields = append(fields, tplField{ID: fmt.Sprintf("t%dr%dc%d", ti, ri, ci+1), Label: r[ci], Kind: "field", Where: fmt.Sprintf("جدول %d", ti+1)})
						}
					}
				}
			}
			ti++
		case "w:p":
			txt := nodeText(doc, ch)
			if m := paraFieldRe.FindStringSubmatch(txt); m != nil && strings.TrimSpace(m[1]) != "" {
				fields = append(fields, tplField{ID: fmt.Sprintf("p%d", pi), Label: strings.TrimSpace(m[1]), Kind: "field", Where: "فقرة"})
			}
			pi++
		}
	}
	return fields, nil
}

func firstNonEmpty(l []string) string {
	for _, s := range l {
		if s != "" {
			return s
		}
	}
	return "table"
}

// setParaXML returns the paragraph with its text replaced by `text` (formatting of the first run is kept).
func setParaXML(p string, text string) string {
	open := p
	if i := strings.IndexByte(p, '>'); i >= 0 {
		open = p[:i+1]
	}
	if strings.HasSuffix(open, "/>") { // <w:p/>
		open = open[:len(open)-2] + ">"
		p = open + "</w:p>"
	}
	ppr := ""
	if m := regexp.MustCompile(`(?s)<w:pPr>.*?</w:pPr>`).FindString(p); m != "" {
		ppr = m
	}
	rpr := ""
	if m := regexp.MustCompile(`(?s)<w:r(?: [^>]*)?>\s*(<w:rPr>.*?</w:rPr>)`).FindStringSubmatch(p); m != nil {
		rpr = m[1]
	}
	if isRTLText(text) && !strings.Contains(rpr, "<w:rtl") {
		if rpr == "" {
			rpr = "<w:rPr><w:rtl/></w:rPr>"
		} else {
			rpr = strings.Replace(rpr, "</w:rPr>", "<w:rtl/></w:rPr>", 1)
		}
	}
	var runs strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			runs.WriteString("<w:r>" + rpr + "<w:br/></w:r>")
		}
		runs.WriteString("<w:r>" + rpr + `<w:t xml:space="preserve">` + xmlEsc(line) + "</w:t></w:r>")
	}
	return open + ppr + runs.String() + "</w:p>"
}

func setCellXML(s string, cell xnode, text string) string {
	ps := childrenOf(s, cell, "w:p")
	if len(ps) == 0 {
		return s[:cell.Inner1] + "<w:p>" + "</w:p>" + s[cell.Inner1:] // fallthrough, filled on next call
	}
	p := ps[0]
	return s[:p.Start] + setParaXML(s[p.Start:p.End], text) + s[p.End:]
}

type tblVals struct {
	Rows [][]string
}

func fillDOCX(path string, vals map[string]interface{}) ([]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	b, ok := zipFile(zr, "word/document.xml")
	if !ok {
		return nil, fmt.Errorf("word/document.xml not found")
	}
	doc := string(b)
	str := func(v interface{}) string {
		switch t := v.(type) {
		case string:
			return t
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		}
		return ""
	}
	getTable := func(d string, ti int) (xnode, bool) {
		body, ok := docxBody(d)
		if !ok {
			return xnode{}, false
		}
		n := 0
		for _, ch := range children(d, body.Inner0, body.Inner1) {
			if ch.Tag == "w:tbl" {
				if n == ti {
					return ch, true
				}
				n++
			}
		}
		return xnode{}, false
	}
	// 1) cell fields
	reCell := regexp.MustCompile(`^t(\d+)r(\d+)c(\d+)$`)
	var ids []string
	for k := range vals {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m := reCell.FindStringSubmatch(id)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(str(vals[id]))
		if text == "" {
			continue
		}
		ti, _ := strconv.Atoi(m[1])
		ri, _ := strconv.Atoi(m[2])
		ci, _ := strconv.Atoi(m[3])
		tbl, ok := getTable(doc, ti)
		if !ok {
			continue
		}
		rows := childrenOf(doc, tbl, "w:tr")
		if ri >= len(rows) {
			continue
		}
		cells := childrenOf(doc, rows[ri], "w:tc")
		if ci >= len(cells) {
			continue
		}
		doc = setCellXML(doc, cells[ci], text)
	}
	// 2) list tables
	for _, id := range ids {
		if !strings.HasPrefix(id, "tt") {
			continue
		}
		ti, err := strconv.Atoi(id[2:])
		if err != nil {
			continue
		}
		rowsIn, _ := vals[id].([]interface{})
		var data [][]string
		for _, r := range rowsIn {
			var row []string
			if arr, ok := r.([]interface{}); ok {
				for _, c := range arr {
					row = append(row, str(c))
				}
			}
			empty := true
			for _, c := range row {
				if strings.TrimSpace(c) != "" {
					empty = false
				}
			}
			if !empty {
				data = append(data, row)
			}
		}
		if len(data) == 0 {
			continue
		}
		for k, row := range data {
			tbl, ok := getTable(doc, ti)
			if !ok {
				break
			}
			rows := childrenOf(doc, tbl, "w:tr")
			ri := k + 1
			for ri >= len(rows) { // clone the last row, emptied
				last := rows[len(rows)-1]
				cl := doc[last.Start:last.End]
				doc = doc[:tbl.Inner1] + cl + doc[tbl.Inner1:]
				tbl, _ = getTable(doc, ti)
				rows = childrenOf(doc, tbl, "w:tr")
			}
			for ci, text := range row {
				tbl, _ = getTable(doc, ti)
				rows = childrenOf(doc, tbl, "w:tr")
				cells := childrenOf(doc, rows[ri], "w:tc")
				if ci >= len(cells) {
					break
				}
				doc = setCellXML(doc, cells[ci], strings.TrimSpace(text))
			}
		}
	}
	// 3) paragraph fields
	for _, id := range ids {
		if !strings.HasPrefix(id, "p") || strings.HasPrefix(id, "pp") {
			continue
		}
		pi, err := strconv.Atoi(id[1:])
		if err != nil {
			continue
		}
		text := strings.TrimSpace(str(vals[id]))
		if text == "" {
			continue
		}
		body, _ := docxBody(doc)
		n := 0
		for _, ch := range children(doc, body.Inner0, body.Inner1) {
			if ch.Tag != "w:p" {
				continue
			}
			if n == pi {
				old := nodeText(doc, ch)
				label := old
				if m := paraFieldRe.FindStringSubmatch(old); m != nil {
					label = strings.TrimSpace(m[1])
				}
				doc = doc[:ch.Start] + setParaXML(doc[ch.Start:ch.End], label+": "+text) + doc[ch.End:]
				break
			}
			n++
		}
	}
	return rewriteZip(zr, map[string][]byte{"word/document.xml": []byte(doc)})
}

func rewriteZip(zr *zip.ReadCloser, repl map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		w, err := zw.Create(f.Name)
		if err != nil {
			return nil, err
		}
		if nb, ok := repl[f.Name]; ok {
			_, _ = w.Write(nb)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		_, _ = io.Copy(w, rc)
		rc.Close()
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ------------------------------------------------------------ XLSX

func splitRef(ref string) (col int, row int) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		col = col*26 + int(ref[i]-'A'+1)
		i++
	}
	row, _ = strconv.Atoi(ref[i:])
	return col - 1, row
}

type xlsxInfo struct {
	Sheet  xlsxSheet
	Rows   [][]xlCell // non-empty cells
	Styled map[int]int
}

func analyzeXLSX(path string) ([]tplField, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	shared := xlsxShared(zr)
	var fields []tplField
	for si, sh := range xlsxSheets(zr) {
		rows := xlsxRows(zr, sh.File, shared)
		if len(rows) == 0 {
			continue
		}
		// grid
		type pos struct{ c, r int }
		cells := map[pos]string{}
		maxRow := 0
		for _, r := range rows {
			for _, c := range r {
				col, row := splitRef(c.Ref)
				cells[pos{col, row}] = c.Value
				if row > maxRow {
					maxRow = row
				}
			}
		}
		raw, _ := zipFile(zr, sh.File)
		existingRows := map[int]bool{}
		for _, m := range regexp.MustCompile(`<row [^>]*r="(\d+)"`).FindAllStringSubmatch(string(raw), -1) {
			n, _ := strconv.Atoi(m[1])
			existingRows[n] = true
		}
		usedAsTable := map[int]bool{}
		// list tables: a row with >=3 consecutive non-empty cells followed by empty/styled rows or end
		rowsSorted := []int{}
		rowSet := map[int]bool{}
		for p := range cells {
			rowSet[p.r] = true
		}
		for r := range rowSet {
			rowsSorted = append(rowsSorted, r)
		}
		sort.Ints(rowsSorted)
		for _, r := range rowsSorted {
			if usedAsTable[r] {
				continue
			}
			// consecutive run
			maxc := 0
			for p := range cells {
				if p.r == r && p.c > maxc {
					maxc = p.c
				}
			}
			run, best, startc, bestStart := 0, 0, 0, 0
			for c := 0; c <= maxc+1; c++ {
				if cells[pos{c, r}] != "" {
					if run == 0 {
						startc = c
					}
					run++
					if run > best {
						best, bestStart = run, startc
					}
				} else {
					run = 0
				}
			}
			if best < 3 {
				continue
			}
			// Is it a header? next row must be empty or a data row of similar width; and it must not be a title row
			if _, numeric := strconv.ParseFloat(cells[pos{bestStart, r}], 64); numeric == nil {
				continue
			}
			var cols []string
			for c := bestStart; c < bestStart+best; c++ {
				cols = append(cols, cells[pos{c, r}])
			}
			// data rows below
			last := r
			for rr := r + 1; rowSet[rr] || existingRows[rr]; rr++ {
				last = rr
				usedAsTable[rr] = true
			}
			usedAsTable[r] = true
			cnt := 0
			for rr := r + 1; rr <= last; rr++ {
				if !rowSet[rr] {
					cnt++
				}
			}
			fields = append(fields, tplField{ID: fmt.Sprintf("x%d|%d|%d|%d", si, r, bestStart, best), Label: sh.Name + " — " + firstNonEmpty(cols), Kind: "table", Cols: cols, Rows: maxInt(cnt, 5), Where: sh.Name})
		}
		for p, v := range cells {
			if usedAsTable[p.r] || v == "" {
				continue
			}
			rowCount := 0
			for q := range cells {
				if q.r == p.r {
					rowCount++
				}
			}
			if rowCount == 1 && !strings.HasSuffix(strings.TrimSpace(v), ":") && !strings.HasSuffix(strings.TrimSpace(v), "：") {
				continue // a title row
			}
			if cells[pos{p.c + 1, p.r}] == "" {
				// label with an empty value cell on its right
				fields = append(fields, tplField{ID: fmt.Sprintf("x%d|%s%d", si, colName(p.c+1), p.r), Label: v, Kind: "field", Where: sh.Name})
			}
		}
	}
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].ID < fields[j].ID })
	return fields, nil
}

func xlsxCellXML(ref, style, text string) string {
	st := ""
	if style != "" {
		st = ` s="` + style + `"`
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf(`<c r="%s"%s/>`, ref, st)
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil && !strings.HasPrefix(strings.TrimSpace(text), "0") || strings.TrimSpace(text) == "0" {
		_ = f
		return fmt.Sprintf(`<c r="%s"%s><v>%s</v></c>`, ref, st, strings.TrimSpace(text))
	}
	return fmt.Sprintf(`<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, st, xmlEsc(text))
}

var rowAttrSpans = regexp.MustCompile(` spans="[^"]*"`)

func setXlsxCell(sheet, ref, text string) string {
	col, rown := splitRef(ref)
	sd0 := strings.Index(sheet, "<sheetData")
	if sd0 < 0 {
		return sheet
	}
	sdEndTag := strings.Index(sheet[sd0:], ">")
	selfClosing := sdEndTag > 0 && sheet[sd0+sdEndTag-1] == '/'
	if selfClosing {
		sheet = sheet[:sd0+sdEndTag-1] + "></sheetData>" + sheet[sd0+sdEndTag+1:]
	}
	sd := children(sheet, 0, len(sheet))
	var sdn xnode
	for _, c := range sd {
		if c.Tag == "worksheet" {
			for _, cc := range children(sheet, c.Inner0, c.Inner1) {
				if cc.Tag == "sheetData" {
					sdn = cc
				}
			}
		}
	}
	if sdn.Tag == "" {
		return sheet
	}
	rows := children(sheet, sdn.Inner0, sdn.Inner1)
	rowNum := func(n xnode) int {
		m := regexp.MustCompile(`^<row [^>]*?\br="(\d+)"`).FindStringSubmatch(sheet[n.Start:n.Inner0])
		if m == nil {
			return -1
		}
		v, _ := strconv.Atoi(m[1])
		return v
	}
	newCellFor := func(style string) string { return xlsxCellXML(ref, style, text) }
	var target *xnode
	insertAt := sdn.Inner1
	for i := range rows {
		rn := rowNum(rows[i])
		if rn == rown {
			target = &rows[i]
			break
		}
		if rn > rown {
			insertAt = rows[i].Start
			break
		}
	}
	if target == nil {
		return sheet[:insertAt] + fmt.Sprintf(`<row r="%d">`, rown) + newCellFor("") + `</row>` + sheet[insertAt:]
	}
	row := *target
	open := sheet[row.Start:row.Inner0]
	selfRow := strings.HasSuffix(open, "/>")
	if selfRow {
		open = open[:len(open)-2] + ">"
		row.Inner0 = row.Start + len(open)
		row.Inner1 = row.Inner0
	}
	open = rowAttrSpans.ReplaceAllString(open, "")
	inner := ""
	if !selfRow {
		inner = sheet[row.Inner0:row.Inner1]
	}
	cells := children(inner, 0, len(inner))
	var out strings.Builder
	placed := false
	for _, c := range cells {
		cr := regexp.MustCompile(`\br="([A-Z]+\d+)"`).FindStringSubmatch(inner[c.Start:c.Inner0])
		cc := -1
		if cr != nil {
			cc, _ = splitRef(cr[1])
		}
		if !placed && cc == col {
			style := ""
			if sm := regexp.MustCompile(`\bs="(\d+)"`).FindStringSubmatch(inner[c.Start:c.Inner0]); sm != nil {
				style = sm[1]
			}
			out.WriteString(newCellFor(style))
			placed = true
			continue
		}
		if !placed && cc > col {
			out.WriteString(newCellFor(""))
			placed = true
		}
		out.WriteString(inner[c.Start:c.End])
	}
	if !placed {
		out.WriteString(newCellFor(""))
	}
	end := row.End
	if selfRow {
		end = target.End
	}
	return sheet[:target.Start] + open + out.String() + "</row>" + sheet[end:]
}

func fillXLSX(path string, vals map[string]interface{}) ([]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	sheets := xlsxSheets(zr)
	repl := map[string][]byte{}
	get := func(si int) (string, bool) {
		if si >= len(sheets) {
			return "", false
		}
		if b, ok := repl[sheets[si].File]; ok {
			return string(b), true
		}
		b, ok := zipFile(zr, sheets[si].File)
		return string(b), ok
	}
	put := func(si int, s string) { repl[sheets[si].File] = []byte(s) }
	var ids []string
	for k := range vals {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, id := range ids {
		parts := strings.Split(strings.TrimPrefix(id, "x"), "|")
		si, err := strconv.Atoi(parts[0])
		if err != nil || !strings.HasPrefix(id, "x") {
			continue
		}
		sh, ok := get(si)
		if !ok {
			continue
		}
		switch len(parts) {
		case 2: // single cell
			t, _ := vals[id].(string)
			if f, ok := vals[id].(float64); ok {
				t = strconv.FormatFloat(f, 'f', -1, 64)
			}
			if strings.TrimSpace(t) == "" {
				continue
			}
			put(si, setXlsxCell(sh, parts[1], strings.TrimSpace(t)))
		case 4: // table: headerRow|startCol|ncols
			hr, _ := strconv.Atoi(parts[1])
			sc, _ := strconv.Atoi(parts[2])
			rowsIn, _ := vals[id].([]interface{})
			// first free row below the header = last non-empty row of the block + 1
			shared := xlsxShared(zr)
			_ = shared
			existing := xlsxRows(&zip.ReadCloser{Reader: zip.Reader{File: zr.File}}, sheets[si].File, shared)
			last := hr
			have := map[int]bool{}
			for _, r := range existing {
				for _, c := range r {
					_, rn := splitRef(c.Ref)
					have[rn] = true
				}
			}
			for rr := hr + 1; have[rr]; rr++ {
				last = rr
			}
			k := 0
			for _, r := range rowsIn {
				arr, _ := r.([]interface{})
				empty := true
				for _, c := range arr {
					if s, _ := c.(string); strings.TrimSpace(s) != "" {
						empty = false
					}
				}
				if empty {
					continue
				}
				k++
				for ci, c := range arr {
					t := ""
					switch v := c.(type) {
					case string:
						t = v
					case float64:
						t = strconv.FormatFloat(v, 'f', -1, 64)
					}
					if strings.TrimSpace(t) == "" {
						continue
					}
					sh, _ = get(si)
					put(si, setXlsxCell(sh, colName(sc+ci)+strconv.Itoa(last+k), strings.TrimSpace(t)))
				}
			}
		}
	}
	return rewriteZip(zr, repl)
}

func analyzeTemplate(path string) ([]tplField, error) {
	switch strings.ToLower(pathExt(path)) {
	case ".docx":
		return analyzeDOCX(path)
	case ".xlsx", ".xlsm":
		return analyzeXLSX(path)
	}
	return nil, fmt.Errorf("القوالب المدعومة: Word (.docx) و Excel (.xlsx)")
}

func fillTemplate(path string, vals map[string]interface{}) ([]byte, error) {
	switch strings.ToLower(pathExt(path)) {
	case ".docx":
		return fillDOCX(path, vals)
	case ".xlsx", ".xlsm":
		return fillXLSX(path, vals)
	}
	return nil, fmt.Errorf("unsupported")
}

func pathExt(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[i:]
	}
	return ""
}

var _ = os.Stat
