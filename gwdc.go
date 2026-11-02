package main

import "strings"

// gwdcClass maps the "Related to" hazard category of the GWDC tracking register to the Sonatrach classification and a default nature.
func gwdcClass(rel string) (class, nature string) {
	l := lc(rel)
	has := func(k ...string) bool {
		for _, x := range k {
			if strings.Contains(l, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("personal protective", "ppe"):
		return "EPI", "Unsafe Act"
	case has("unsafe action"):
		return "Réaction des Personnes", "Unsafe Act"
	case has("housekeeping", "house/", "storage", "welfare", "signs"):
		return "Procédures & Ordre", "Unsafe Condition"
	case has("procedure", "ordre", "policy"):
		return "Procédures & Ordre", "Unsafe Act"
	case has("lifting"):
		return "Procédures & Ordre", "Unsafe Act"
	case has("manual hand"):
		return "Position dangereuse", "Unsafe Act"
	case has("trip", "slip", "height", "drop", "barrier", "access", "ergonomic", "working / local"):
		return "Position dangereuse", "Unsafe Condition"
	case has("equipment", "machinery", "tool", "electrical", "installation", "pressur", "fire", "safety equip"):
		return "Outils & matériels", "Unsafe Condition"
	case has("environment", "hazardous", "substance", "transport", "road"):
		return "Environnement", "Unsafe Condition"
	}
	return "Autres", "Unsafe Condition"
}
