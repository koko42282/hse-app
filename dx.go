package main

// Small DOCX builder for the HSE forms (tables, shaded labels, photos).

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

type dxCell struct {
	Lines []string
	Bold  bool
	Shade string // hex fill
	Span  int
	Size  int // half-points
	Align string
	Img   []byte
	ImgW  float64 // cm
}

type dx struct {
	body strings.Builder
	imgs [][]byte
	rtl  bool
}

func newDx(rtl bool) *dx { return &dx{rtl: rtl} }

func (d *dx) para(text string, bold bool, size int, align string) {
	ppr := `<w:spacing w:after="80"/>`
	if align != "" {
		ppr += `<w:jc w:val="` + align + `"/>`
	}
	if d.rtl || isRTLText(text) {
		ppr = "<w:bidi/>" + ppr
	}
	d.body.WriteString("<w:p><w:pPr>" + ppr + "</w:pPr>" + docxRun(text, bold, size, d.rtl || isRTLText(text)) + "</w:p>")
}

func (d *dx) imageXML(b []byte, wcm float64) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width == 0 {
		return ""
	}
	d.imgs = append(d.imgs, b)
	n := len(d.imgs)
	cx := int(wcm * 360000)
	cy := cx * cfg.Height / cfg.Width
	maxH := 9 * 360000
	if cy > maxH {
		cx = cx * maxH / cy
		cy = maxH
	}
	return fmt.Sprintf(`<w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="Photo %d"/><a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:nvPicPr><pic:cNvPr id="%d" name="p%d.jpg"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="rIdImg%d"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>`, cx, cy, n, n, n, n, n, cx, cy)
}

// table: widths in twips (sum ~ 10200 for A4 with 1.5cm margins)
func (d *dx) table(widths []int, rows [][]dxCell) {
	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/>`)
	if d.rtl {
		b.WriteString(`<w:bidiVisual/>`)
	}
	b.WriteString(`<w:tblBorders>`)
	for _, s := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		b.WriteString(`<w:` + s + ` w:val="single" w:sz="6" w:space="0" w:color="444444"/>`)
	}
	b.WriteString(`</w:tblBorders><w:tblLayout w:type="fixed"/><w:tblCellMar><w:left w:w="80" w:type="dxa"/><w:right w:w="80" w:type="dxa"/></w:tblCellMar></w:tblPr><w:tblGrid>`)
	for _, w := range widths {
		fmt.Fprintf(&b, `<w:gridCol w:w="%d"/>`, w)
	}
	b.WriteString(`</w:tblGrid>`)
	for _, row := range rows {
		b.WriteString(`<w:tr><w:trPr><w:cantSplit/></w:trPr>`)
		col := 0
		for _, c := range row {
			span := c.Span
			if span < 1 {
				span = 1
			}
			w := 0
			for i := col; i < col+span && i < len(widths); i++ {
				w += widths[i]
			}
			col += span
			b.WriteString(`<w:tc><w:tcPr>`)
			fmt.Fprintf(&b, `<w:tcW w:w="%d" w:type="dxa"/>`, w)
			if span > 1 {
				fmt.Fprintf(&b, `<w:gridSpan w:val="%d"/>`, span)
			}
			if c.Shade != "" {
				b.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="` + c.Shade + `"/>`)
			}
			b.WriteString(`<w:vAlign w:val="center"/></w:tcPr>`)
			size := c.Size
			if size == 0 {
				size = 20
			}
			if c.Img != nil {
				w := c.ImgW
				if w == 0 {
					w = 8
				}
				b.WriteString(`<w:p><w:pPr><w:jc w:val="center"/></w:pPr>` + d.imageXML(c.Img, w) + `</w:p>`)
			}
			lines := c.Lines
			if len(lines) == 0 && c.Img == nil {
				lines = []string{""}
			}
			for _, ln := range lines {
				ppr := `<w:spacing w:before="20" w:after="20"/>`
				if c.Align != "" {
					ppr += `<w:jc w:val="` + c.Align + `"/>`
				}
				rtl := d.rtl || isRTLText(ln)
				if rtl {
					ppr = "<w:bidi/>" + ppr
				}
				b.WriteString("<w:p><w:pPr>" + ppr + "</w:pPr>" + docxRun(ln, c.Bold, size, rtl) + "</w:p>")
			}
			b.WriteString(`</w:tc>`)
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl><w:p/>`)
	d.body.WriteString(b.String())
}

func lbl(s string) dxCell { return dxCell{Lines: []string{s}, Bold: true, Shade: "DCE6F1"} }
func txt(s string) dxCell { return dxCell{Lines: strings.Split(s, "\n")} }
func head(s string, span int) dxCell {
	return dxCell{Lines: []string{s}, Bold: true, Shade: "1F3864", Span: span, Size: 22, Align: "center"}
}

// photoGrid lays photos out two per row.
func (d *dx) photoGrid(widths []int, photos [][]byte) {
	var rows [][]dxCell
	for i := 0; i < len(photos); i += 2 {
		r := []dxCell{{Img: photos[i], ImgW: 8}}
		if i+1 < len(photos) {
			r = append(r, dxCell{Img: photos[i+1], ImgW: 8})
		} else {
			r = append(r, dxCell{Lines: []string{""}})
		}
		rows = append(rows, r)
	}
	if len(rows) > 0 {
		d.table(widths, rows)
	}
}

func (d *dx) bytes() []byte {
	z := newZip()
	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="jpg" ContentType="image/jpeg"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`
	z.add("[Content_Types].xml", ct)
	z.add("_rels/.rels", xmlHdr+`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`)
	var rels strings.Builder
	rels.WriteString(xmlHdr + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := range d.imgs {
		fmt.Fprintf(&rels, `<Relationship Id="rIdImg%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/p%d.jpg"/>`, i+1, i+1)
	}
	rels.WriteString(`</Relationships>`)
	z.add("word/_rels/document.xml.rels", rels.String())
	for i, b := range d.imgs {
		z.add(fmt.Sprintf("word/media/p%d.jpg", i+1), string(b))
	}
	doc := xmlHdr + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:body>` + d.body.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="850" w:right="850" w:bottom="850" w:left="850" w:header="400" w:footer="400" w:gutter="0"/></w:sectPr></w:body></w:document>`
	z.add("word/document.xml", doc)
	return z.bytes()
}
