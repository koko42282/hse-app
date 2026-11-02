package main

// trStops translates the description / action / zone of STOP cards into the report language.
func (a *app) trStops(cards []StopCard, lang string) ([]StopCard, bool) {
	out := append([]StopCard(nil), cards...)
	var txt []string
	for _, c := range out {
		txt = append(txt, c.Desc, c.Action)
	}
	res, ok := a.translateTexts(txt, lang)
	for i := range out {
		out[i].Desc, out[i].Action = res[2*i], res[2*i+1]
	}
	return out, ok
}
