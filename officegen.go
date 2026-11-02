package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"unicode"
)

// ---- tiny markdown-ish parser (headings, bullets, numbered lists, pipe tables) ----

type mdBlock struct {
	Kind  string // h1 h2 h3 p li table
	Text  string
	Table [][]string
}

func cleanInline(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	return strings.TrimSpace(s)
}

func parseMD(text string) []mdBlock {
	var out []mdBlock
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "|") {
			var rows [][]string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				r := strings.TrimSpace(lines[i])
				r = strings.Trim(r, "|")
				cells := strings.Split(r, "|")
				sep := true
				for k := range cells {
					cells[k] = cleanInline(cells[k])
					if strings.Trim(cells[k], "-: ") != "" {
						sep = false
					}
				}
				if !sep {
					rows = append(rows, cells)
				}
			}
			i--
			if len(rows) > 0 {
				out = append(out, mdBlock{Kind: "table", Table: rows})
			}
			continue
		}
		switch {
		case strings.HasPrefix(l, "### "):
			out = append(out, mdBlock{Kind: "h3", Text: cleanInline(l[4:])})
		case strings.HasPrefix(l, "## "):
			out = append(out, mdBlock{Kind: "h2", Text: cleanInline(l[3:])})
		case strings.HasPrefix(l, "# "):
			out = append(out, mdBlock{Kind: "h1", Text: cleanInline(l[2:])})
		case strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") || strings.HasPrefix(l, "• "):
			out = append(out, mdBlock{Kind: "li", Text: cleanInline(string([]rune(l)[2:]))})
		default:
			out = append(out, mdBlock{Kind: "p", Text: cleanInline(l)})
		}
	}
	return out
}

func isRTLText(s string) bool {
	ar, la := 0, 0
	for _, r := range s {
		if isArabicRune(r) {
			ar++
		} else if unicode.IsLetter(r) {
			la++
		}
	}
	return ar > 0 && ar >= la
}

func xmlEsc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			if r == '\t' || r == '\n' || r >= 0x20 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

type zipOut struct {
	buf bytes.Buffer
	zw  *zip.Writer
}

func newZip() *zipOut {
	z := &zipOut{}
	z.zw = zip.NewWriter(&z.buf)
	return z
}
func (z *zipOut) add(name, content string) {
	w, _ := z.zw.Create(name)
	_, _ = w.Write([]byte(content))
}
func (z *zipOut) bytes() []byte { _ = z.zw.Close(); return z.buf.Bytes() }

const xmlHdr = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// ======================================================================= DOCX

func docxRun(text string, bold bool, size int, rtl bool) string {
	rpr := ""
	if bold {
		rpr += "<w:b/><w:bCs/>"
	}
	if size > 0 {
		rpr += fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, size, size)
	}
	if rtl {
		rpr += "<w:rtl/>"
	}
	if rpr != "" {
		rpr = "<w:rPr>" + rpr + "</w:rPr>"
	}
	return `<w:r>` + rpr + `<w:t xml:space="preserve">` + xmlEsc(text) + `</w:t></w:r>`
}

func docxPara(text string, bold bool, size int, extraPPr string) string {
	rtl := isRTLText(text)
	ppr := extraPPr
	if rtl {
		ppr = "<w:bidi/>" + ppr
	}
	if ppr != "" {
		ppr = "<w:pPr>" + ppr + "</w:pPr>"
	}
	return "<w:p>" + ppr + docxRun(text, bold, size, rtl) + "</w:p>"
}

func buildDOCX(title, md string) []byte {
	blocks := parseMD(md)
	var body strings.Builder
	if title != "" {
		body.WriteString(docxPara(title, true, 36, `<w:spacing w:after="200"/>`))
	}
	for _, b := range blocks {
		switch b.Kind {
		case "h1":
			body.WriteString(docxPara(b.Text, true, 32, `<w:keepNext/><w:spacing w:before="240" w:after="120"/>`))
		case "h2":
			body.WriteString(docxPara(b.Text, true, 28, `<w:keepNext/><w:spacing w:before="200" w:after="100"/>`))
		case "h3":
			body.WriteString(docxPara(b.Text, true, 24, `<w:keepNext/><w:spacing w:before="160" w:after="80"/>`))
		case "li":
			body.WriteString(docxPara("• "+b.Text, false, 0, `<w:ind w:left="360" w:hanging="260"/>`))
		case "p":
			body.WriteString(docxPara(b.Text, false, 0, `<w:spacing w:after="100"/>`))
		case "table":
			cols := 0
			rtl := false
			for _, r := range b.Table {
				if len(r) > cols {
					cols = len(r)
				}
				for _, c := range r {
					if isRTLText(c) {
						rtl = true
					}
				}
			}
			bd := `<w:tblBorders>`
			for _, e := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
				bd += `<w:` + e + ` w:val="single" w:sz="4" w:space="0" w:color="808080"/>`
			}
			bd += `</w:tblBorders>`
			vis := ""
			if rtl {
				vis = "<w:bidiVisual/>"
			}
			body.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="5000" w:type="pct"/>` + vis + bd + `<w:tblCellMar><w:left w:w="80" w:type="dxa"/><w:right w:w="80" w:type="dxa"/></w:tblCellMar></w:tblPr><w:tblGrid>`)
			for c := 0; c < cols; c++ {
				body.WriteString(fmt.Sprintf(`<w:gridCol w:w="%d"/>`, 9000/maxInt(cols, 1)))
			}
			body.WriteString(`</w:tblGrid>`)
			for ri, r := range b.Table {
				body.WriteString(`<w:tr>`)
				if ri == 0 {
					body.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
				}
				for c := 0; c < cols; c++ {
					v := ""
					if c < len(r) {
						v = r[c]
					}
					shd := ""
					if ri == 0 {
						shd = `<w:shd w:val="clear" w:color="auto" w:fill="D9E2F3"/>`
					}
					body.WriteString(`<w:tc><w:tcPr>` + shd + `</w:tcPr>` + docxPara(v, ri == 0, 0, "") + `</w:tc>`)
				}
				body.WriteString(`</w:tr>`)
			}
			body.WriteString(`</w:tbl><w:p/>`)
		}
	}
	z := newZip()
	z.add("[Content_Types].xml", xmlHdr+`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/></Types>`)
	z.add("_rels/.rels", xmlHdr+`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`)
	z.add("word/_rels/document.xml.rels", xmlHdr+`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`)
	z.add("word/styles.xml", xmlHdr+`<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:cs="Arial" w:eastAsia="SimSun"/><w:sz w:val="22"/><w:szCs w:val="22"/><w:lang w:val="en-US" w:bidi="ar-SA" w:eastAsia="zh-CN"/></w:rPr></w:rPrDefault></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style></w:styles>`)
	z.add("word/document.xml", xmlHdr+`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+body.String()+`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr></w:body></w:document>`)
	return z.bytes()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ======================================================================= XLSX

func colName(i int) string { // 0 -> A
	s := ""
	for i >= 0 {
		s = string(rune('A'+i%26)) + s
		i = i/26 - 1
	}
	return s
}

// ======================================================================= PPTX

func pptPara(text string, size int, bold bool, bullet bool) string {
	rtl := isRTLText(text)
	algn := "l"
	rtlA := ""
	if rtl {
		algn = "r"
		rtlA = ` rtl="1"`
	}
	bu := `<a:buNone/>`
	ind := ""
	if bullet {
		bu = `<a:buChar char="•"/>`
		ind = ` marL="285750" indent="-285750"`
	}
	b := ""
	if bold {
		b = ` b="1"`
	}
	return fmt.Sprintf(`<a:p><a:pPr algn="%s"%s%s>%s</a:pPr><a:r><a:rPr lang="en-US" sz="%d"%s dirty="0"><a:latin typeface="Calibri"/><a:cs typeface="Arial"/><a:ea typeface="SimSun"/></a:rPr><a:t>%s</a:t></a:r></a:p>`, algn, rtlA, ind, bu, size, b, xmlEsc(text))
}

func pptShape(id int, name string, x, y, cx, cy int, paras string) string {
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr><p:txBody><a:bodyPr wrap="square" rtlCol="0"><a:normAutofit/></a:bodyPr><a:lstStyle/>%s</p:txBody></p:sp>`, id, name, x, y, cx, cy, paras)
}

func buildPPTX(title, md string) []byte {
	type slide struct {
		title string
		lines []mdBlock
	}
	var slides []slide
	cur := slide{title: title}
	started := title != ""
	flush := func() {
		if started || len(cur.lines) > 0 {
			slides = append(slides, cur)
		}
	}
	for _, b := range parseMD(md) {
		if b.Kind == "h1" || b.Kind == "h2" {
			if started || len(cur.lines) > 0 {
				flush()
			}
			cur = slide{title: b.Text}
			started = true
			continue
		}
		cur.lines = append(cur.lines, b)
	}
	flush()
	if len(slides) == 0 {
		slides = []slide{{title: title}}
	}
	// split over-long slides
	var final []slide
	for _, s := range slides {
		items := []string{}
		for _, b := range s.lines {
			if b.Kind == "table" {
				for _, r := range b.Table {
					items = append(items, strings.Join(r, "  |  "))
				}
			} else {
				items = append(items, b.Text)
			}
		}
		for len(items) > 9 {
			final = append(final, slide{title: s.title, lines: toLines(items[:9])})
			items = items[9:]
		}
		final = append(final, slide{title: s.title, lines: toLines(items)})
	}
	z := newZip()
	ct := `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/><Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/><Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/><Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`
	for i := range final {
		ct += fmt.Sprintf(`<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, i+1)
	}
	ct += `</Types>`
	z.add("[Content_Types].xml", xmlHdr+ct)
	z.add("_rels/.rels", xmlHdr+`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/></Relationships>`)
	ids := ""
	prel := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>`
	for i := range final {
		ids += fmt.Sprintf(`<p:sldId id="%d" r:id="rId%d"/>`, 256+i, 10+i)
		prel += fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, 10+i, i+1)
	}
	prel += `</Relationships>`
	z.add("ppt/_rels/presentation.xml.rels", xmlHdr+prel)
	z.add("ppt/presentation.xml", xmlHdr+`<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst><p:sldIdLst>`+ids+`</p:sldIdLst><p:sldSz cx="12192000" cy="6858000"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>`)
	nsP := `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"`
	grp := `<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`
	z.add("ppt/slideMasters/slideMaster1.xml", xmlHdr+`<p:sldMaster `+nsP+`><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:effectLst/></p:bgPr></p:bg><p:spTree>`+grp+`</p:spTree></p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/><p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst></p:sldMaster>`)
	z.add("ppt/slideMasters/_rels/slideMaster1.xml.rels", xmlHdr+`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/></Relationships>`)
	z.add("ppt/slideLayouts/slideLayout1.xml", xmlHdr+`<p:sldLayout `+nsP+` type="blank" preserve="1"><p:cSld name="Blank"><p:spTree>`+grp+`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`)
	z.add("ppt/slideLayouts/_rels/slideLayout1.xml.rels", xmlHdr+`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`)
	fill3 := `<a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill>`
	ln3 := `<a:ln w="6350"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln w="12700"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln w="19050"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln>`
	ef3 := `<a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle>`
	z.add("ppt/theme/theme1.xml", xmlHdr+`<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Office"><a:themeElements><a:clrScheme name="Office"><a:dk1><a:srgbClr val="000000"/></a:dk1><a:lt1><a:srgbClr val="FFFFFF"/></a:lt1><a:dk2><a:srgbClr val="1F3864"/></a:dk2><a:lt2><a:srgbClr val="E7E6E6"/></a:lt2><a:accent1><a:srgbClr val="2F5597"/></a:accent1><a:accent2><a:srgbClr val="ED7D31"/></a:accent2><a:accent3><a:srgbClr val="A5A5A5"/></a:accent3><a:accent4><a:srgbClr val="FFC000"/></a:accent4><a:accent5><a:srgbClr val="5B9BD5"/></a:accent5><a:accent6><a:srgbClr val="70AD47"/></a:accent6><a:hlink><a:srgbClr val="0563C1"/></a:hlink><a:folHlink><a:srgbClr val="954F72"/></a:folHlink></a:clrScheme><a:fontScheme name="Office"><a:majorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface="Arial"/></a:majorFont><a:minorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface="Arial"/></a:minorFont></a:fontScheme><a:fmtScheme name="Office"><a:fillStyleLst>`+fill3+`</a:fillStyleLst><a:lnStyleLst>`+ln3+`</a:lnStyleLst><a:effectStyleLst>`+ef3+`</a:effectStyleLst><a:bgFillStyleLst>`+fill3+`</a:bgFillStyleLst></a:fmtScheme></a:themeElements></a:theme>`)
	for i, s := range final {
		var body strings.Builder
		for _, b := range s.lines {
			body.WriteString(pptPara(b.Text, 2000, false, b.Kind == "li" || b.Kind == "p"))
		}
		if body.Len() == 0 {
			body.WriteString(pptPara("", 2000, false, false))
		}
		sp := pptShape(2, "Title", 500000, 300000, 11192000, 1000000, pptPara(s.title, 3200, true, false)) +
			pptShape(3, "Content", 500000, 1500000, 11192000, 4900000, body.String())
		z.add(fmt.Sprintf("ppt/slides/slide%d.xml", i+1), xmlHdr+`<p:sld `+nsP+`><p:cSld><p:spTree>`+grp+sp+`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`)
		z.add(fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", i+1), xmlHdr+`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/></Relationships>`)
	}
	return z.bytes()
}

func toLines(items []string) []mdBlock {
	var out []mdBlock
	for _, it := range items {
		out = append(out, mdBlock{Kind: "li", Text: it})
	}
	return out
}
