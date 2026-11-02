package main

import "github.com/ledongthuc/pdf"

// pdfPageHasImage reports whether the page references a picture (XObject of subtype Image),
// directly or inside a form XObject (one level).
func pdfPageHasImage(p pdf.Page) (has bool) {
	defer func() {
		if recover() != nil {
			has = false
		}
	}()
	xo := p.Resources().Key("XObject")
	if xo.Kind() != pdf.Dict {
		return false
	}
	for _, k := range xo.Keys() {
		o := xo.Key(k)
		switch o.Key("Subtype").Name() {
		case "Image":
			return true
		case "Form":
			x2 := o.Key("Resources").Key("XObject")
			if x2.Kind() == pdf.Dict {
				for _, k2 := range x2.Keys() {
					if x2.Key(k2).Key("Subtype").Name() == "Image" {
						return true
					}
				}
			}
		}
	}
	return false
}
