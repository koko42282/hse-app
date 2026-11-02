package main

import (
	"hash/fnv"
	"net/http"
	"sort"
)

// stopEval: hold-out accuracy of the small classifiers (80% train / 20% test, split by a hash of the card).
func (h *hubStore) stopEval() map[string]interface{} {
	h.mu.Lock()
	cards := append([]StopCard{}, h.h.Stops...)
	h.mu.Unlock()
	out := map[string]interface{}{}
	for _, sch := range []string{"sonatrach", "sinopec", ""} {
		var trD, teD [][]string
		var trN, trC, trR, teN, teC, teR []string
		for _, c := range cards {
			if sch != "" && c.Scheme != sch || c.Class == "" || c.Nature == "" {
				continue
			}
			f := fnv.New32a()
			f.Write([]byte(stopKey(c.Date, c.Desc)))
			if f.Sum32()%5 == 0 {
				teD, teN, teC, teR = append(teD, stopText(c)), append(teN, c.Nature), append(teC, c.Class), append(teR, c.Risk)
			} else {
				trD, trN, trC, trR = append(trD, stopText(c)), append(trN, c.Nature), append(trC, c.Class), append(trR, c.Risk)
			}
		}
		name := sch
		if name == "" {
			name = "all"
		}
		if len(trD) < 50 || len(teD) < 10 {
			out[name] = map[string]interface{}{"train": len(trD), "test": len(teD)}
			continue
		}
		mn, mc, mr := trainLin(trD, trN), trainLin(trD, trC), trainLin(trD, trR)
		ok := [3]int{}
		hi, hiOK := 0, 0
		for i, d := range teD {
			n, cn := mn.predict(d)
			c, cc := mc.predict(d)
			r, _ := mr.predict(d)
			if n == teN[i] {
				ok[0]++
			}
			if c == teC[i] {
				ok[1]++
			}
			if r == teR[i] {
				ok[2]++
			}
			if cc >= 0.7 && cn >= 0.7 {
				hi++
				if c == teC[i] && n == teN[i] {
					hiOK++
				}
			}
		}
		t := float64(len(teD))
		out[name] = map[string]interface{}{"train": len(trD), "test": len(teD), "nature": float64(ok[0]) / t, "class": float64(ok[1]) / t, "risk": float64(ok[2]) / t,
			"confident": hi, "confidentOK": hiOK}
	}
	return out
}

func (a *app) stopEvalHandler(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/stopdb/eval", guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.hub.stopEval())
	}))
}

var _ = sort.Strings
