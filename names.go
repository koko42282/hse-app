package main

import (
	"fmt"
	"regexp"
	"strings"
)

// File names of Sonatrach deliverables keep the ORIGINAL names; only the date and the report number change.
var (
	reDash8  = regexp.MustCompile(`\d{2}-\d{2}-\d{4}`)
	reCompct = regexp.MustCompile(`\d{8}`)
	reRepNo  = regexp.MustCompile(`(?i)(N\s*[°º_]\s*)\d+`)
)

// renameLike takes an original file name and gives it the new date (and report number when n != "").
func renameLike(orig, date, n string) string {
	d := dmy(date)
	out := reDash8.ReplaceAllString(orig, d)
	out = reCompct.ReplaceAllString(out, strings.ReplaceAll(d, "-", ""))
	if n != "" {
		out = reRepNo.ReplaceAllString(out, "${1}"+n)
	}
	return out
}

func rigNum(rig string) string {
	var b strings.Builder
	for _, r := range rig {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return strings.ReplaceAll(rig, " ", "")
	}
	return b.String()
}

func wellSp(well string) string { return strings.ReplaceAll(well, "-", " ") }

// default names = the ones Sonatrach already receives
func (a *app) defaultName(kind string) string {
	rig, well := a.rigWell()
	rn, w := rigNum(rig), wellSp(well)
	switch kind {
	case "daily":
		return fmt.Sprintf("HSE DAILY REPORT N°00-SPEC %s-%s-01012026.xlsx", rn, w)
	case "pob":
		return fmt.Sprintf("POB SP%s-%s 01-01-2026.xlsx", rn, w)
	case "stop":
		return "Stop cardS log 01012026.xlsx"
	case "ptw":
		return fmt.Sprintf("SSA-HSSE Permit To Work -SP%s.xlsx", rn)
	case "stopreg":
		return fmt.Sprintf("SP%s-SSA-HSE-FOM- STOP Cards Register and Analysis.xls", rn)
	}
	return kind + ".xlsx"
}

// outName: learned original name (setting "name_<kind>") or default, with the new date / number.
func (a *app) outName(kind, date, n string) string {
	o := a.setting("name_" + kind)
	if o == "" {
		o = a.defaultName(kind)
	}
	return renameLike(o, date, n)
}

// learnName remembers the original name of an uploaded Sonatrach file.
func (a *app) learnName(kind, orig string) {
	orig = strings.TrimSpace(orig)
	if orig == "" {
		return
	}
	a.hub.mu.Lock()
	a.hub.h.Settings["name_"+kind] = orig
	_ = a.hub.saveLocked()
	a.hub.mu.Unlock()
}
