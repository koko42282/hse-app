package main

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func zipFile(zr *zip.ReadCloser, name string) ([]byte, bool) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, false
			}
			defer rc.Close()
			b, err := io.ReadAll(io.LimitReader(rc, 200<<20))
			return b, err == nil
		}
	}
	return nil, false
}

// ---------------------------------------------------------------- PPTX

var slideRe = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func xmlTexts(b []byte) string {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var sb strings.Builder
	inT := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inT = true
			} else if t.Name.Local == "br" {
				sb.WriteString("\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				sb.WriteString("\n")
			case "tc":
				sb.WriteString(" | ")
			}
		case xml.CharData:
			if inT {
				sb.Write(t)
			}
		}
	}
	return sb.String()
}

func extractPPTX(p string) ([]pageText, error) { return extractPPTXx(p, false) }

// keepImg: also keep slides that contain only pictures (the OCR tab reads them)
func extractPPTXx(p string, keepImg bool) ([]pageText, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	type sl struct {
		n    int
		name string
	}
	var slides []sl
	for _, f := range zr.File {
		if m := slideRe.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			slides = append(slides, sl{n, f.Name})
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].n < slides[j].n })
	var out []pageText
	for _, s := range slides {
		b, ok := zipFile(zr, s.name)
		if !ok {
			continue
		}
		txt := strings.TrimSpace(xmlTexts(b))
		if nb, ok := zipFile(zr, fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", s.n)); ok {
			if nt := strings.TrimSpace(xmlTexts(nb)); nt != "" {
				txt += "\n\nملاحظات / Notes: " + nt
			}
		}
		rels := relTargets(zr, fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", s.n), "ppt/slides")
		var ids []string
		for id := range rels {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if okImage(zr, rels[id]) {
				txt += "\n\n" + imgMark(rels[id])
			} else if chartEntryRe.MatchString(rels[id]) {
				txt += chartMarks(zr, []string{rels[id]})
			}
		}
		if keepImg || strings.TrimSpace(imgMarker.ReplaceAllString(txt, "")) != "" {
			out = append(out, pageText{Page: s.n, Text: strings.ReplaceAll(txt, "\n", "\n\n")})
		}
	}
	if len(out) == 0 {
		return []pageText{{Page: 0, Text: ""}}, nil
	}
	return out, nil
}

// ---------------------------------------------------------------- XLSX

type xlsxSheet struct {
	Name string
	File string
}

func xlsxSheets(zr *zip.ReadCloser) []xlsxSheet {
	wb, ok := zipFile(zr, "xl/workbook.xml")
	if !ok {
		return nil
	}
	rels, _ := zipFile(zr, "xl/_rels/workbook.xml.rels")
	relTarget := map[string]string{}
	{
		dec := xml.NewDecoder(strings.NewReader(string(rels)))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
				var id, tg string
				for _, a := range se.Attr {
					if a.Name.Local == "Id" {
						id = a.Value
					}
					if a.Name.Local == "Target" {
						tg = a.Value
					}
				}
				relTarget[id] = tg
			}
		}
	}
	var res []xlsxSheet
	dec := xml.NewDecoder(strings.NewReader(string(wb)))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			var name, rid string
			for _, a := range se.Attr {
				if a.Name.Local == "name" {
					name = a.Value
				}
				if a.Name.Local == "id" {
					rid = a.Value
				}
			}
			t := relTarget[rid]
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "/") {
				t = t[1:]
			} else {
				t = path.Join("xl", t)
			}
			res = append(res, xlsxSheet{name, t})
		}
	}
	return res
}

func xlsxShared(zr *zip.ReadCloser) []string {
	b, ok := zipFile(zr, "xl/sharedStrings.xml")
	if !ok {
		return nil
	}
	var out []string
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var cur strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "si" {
				inSI = true
				cur.Reset()
			} else if t.Name.Local == "t" && inSI {
				inT = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inT = false
			} else if t.Name.Local == "si" {
				inSI = false
				out = append(out, cur.String())
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
	return out
}

type xlCell struct {
	Ref   string
	Value string
}

// xlsxRows returns the non-empty cells of a sheet row by row.
func xlsxRows(zr *zip.ReadCloser, file string, shared []string) [][]xlCell {
	b, ok := zipFile(zr, file)
	if !ok {
		return nil
	}
	var rows [][]xlCell
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var row []xlCell
	var ref, typ, val string
	inV, inT, inC := false, false, false
	var inl strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = nil
			case "c":
				inC = true
				ref, typ, val = "", "", ""
				inl.Reset()
				for _, a := range t.Attr {
					if a.Name.Local == "r" {
						ref = a.Value
					}
					if a.Name.Local == "t" {
						typ = a.Value
					}
				}
			case "v":
				inV = true
			case "t":
				if inC {
					inT = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				inC = false
				v := val
				if typ == "s" {
					if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				} else if typ == "inlineStr" {
					v = inl.String()
				}
				v = strings.TrimSpace(v)
				if v != "" {
					row = append(row, xlCell{ref, v})
				}
			case "row":
				if len(row) > 0 {
					rows = append(rows, row)
				}
			}
		case xml.CharData:
			if inV {
				val += string(t)
			} else if inT {
				inl.Write(t)
			}
		}
	}
	return rows
}

func extractXLSX(p string) ([]pageText, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	shared := xlsxShared(zr)
	var out []pageText
	for i, sh := range xlsxSheets(zr) {
		rows := xlsxRows(zr, sh.File, shared)
		if len(rows) == 0 {
			continue
		}
		var sb strings.Builder
		sb.WriteString("[" + sh.Name + "]\n\n")
		for _, r := range rows {
			var parts []string
			for _, c := range r {
				parts = append(parts, c.Value)
			}
			sb.WriteString(strings.Join(parts, " | ") + "\n\n")
		}
		imgs, charts := drawingEntries(zr, path.Join(path.Dir(sh.File), "_rels", path.Base(sh.File)+".rels"), path.Dir(sh.File))
		sb.WriteString(chartMarks(zr, charts))
		for _, im := range imgs {
			sb.WriteString("\n\n" + imgMark(im))
		}
		out = append(out, pageText{Page: i + 1, Text: sb.String()})
	}
	if len(out) == 0 {
		return []pageText{{Page: 0, Text: ""}}, nil
	}
	return out, nil
}

func extractCSV(p string) ([]pageText, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	t := strings.TrimPrefix(string(b), "\xef\xbb\xbf")
	t = strings.ReplaceAll(strings.ReplaceAll(t, "\r", ""), ";", " | ")
	t = strings.ReplaceAll(t, ",", " | ")
	return []pageText{{Page: 0, Text: strings.ReplaceAll(t, "\n", "\n\n")}}, nil
}

// ---------------------------------------------------------------- images

var imgExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true}

// relTargets maps relationship ids to zip entry names (resolved against baseDir).
func relTargets(zr *zip.ReadCloser, relsPath, baseDir string) map[string]string {
	out := map[string]string{}
	b, ok := zipFile(zr, relsPath)
	if !ok {
		return out
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			var id, tg string
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "Id":
					id = a.Value
				case "Target":
					tg = a.Value
				}
			}
			if strings.HasPrefix(tg, "/") {
				tg = tg[1:]
			} else {
				tg = path.Join(baseDir, tg)
			}
			out[id] = tg
		}
	}
	return out
}

// okImage: a displayable picture that is big enough to be content (not a bullet/icon).
func okImage(zr *zip.ReadCloser, name string) bool {
	if !imgExts[strings.ToLower(path.Ext(name))] {
		return false
	}
	for _, f := range zr.File {
		if f.Name == name {
			return f.UncompressedSize64 >= 4096
		}
	}
	return false
}

// dropRepeatedImages removes pictures that repeat on many passages (logos, headers).
func dropRepeatedImages(cs []chunk) {
	cnt := map[string]int{}
	for _, c := range cs {
		seen := map[string]bool{}
		for _, im := range c.Img {
			if !seen[im] {
				seen[im] = true
				cnt[im]++
			}
		}
	}
	for i := range cs {
		var keep []string
		for _, im := range cs[i].Img {
			if im == "@self" || cnt[im] < 3 || len(cs) < 6 {
				keep = append(keep, im)
			}
		}
		cs[i].Img = keep
	}
}

// extractImageFile indexes a standalone picture (sign, pictogram, card, diagram) by its file/folder name.
func extractImageFile(p string) ([]pageText, error) {
	name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	dir := filepath.Base(filepath.Dir(p))
	r := strings.NewReplacer("_", " ", "-", " ", ".", " ")
	txt := r.Replace(name)
	if dir != "rag" && dir != "." {
		txt = r.Replace(dir) + " " + txt
	}
	return []pageText{{Page: 0, Text: txt + ". " + imgMark("@self")}}, nil
}
