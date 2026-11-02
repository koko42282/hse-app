package main

import (
	"encoding/json"
	"net/http"
)

var profileKeys = []string{"hseName", "hseFunction", "hseCompany", "hsePhone", "reportLang", "model_pref", "trQuality"}

// seedProfile gives a fresh install its user profile.
func (a *app) seedProfile() {
	a.hub.mu.Lock()
	defer a.hub.mu.Unlock()
	s := a.hub.h.Settings
	if s["hseName"] == "" || s["hseName"] == "Hamid Bouchrit" {
		s["hseName"] = "Boucherit Hamid"
		if s["hseFunction"] == "" {
			s["hseFunction"] = "HSE"
		}
		_ = a.hub.saveLocked()
	}
	if s["reportLang"] == "" {
		s["reportLang"] = "fr"
	}
}

func (a *app) profileHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	a.seedProfile()
	mux.HandleFunc("/api/hse/profile", guard(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var in map[string]string
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
				http.Error(w, "bad request", 400)
				return
			}
			a.hub.mu.Lock()
			for _, k := range profileKeys {
				if v, ok := in[k]; ok {
					if k == "reportLang" && trLangs[v] == "" {
						v = "fr"
					}
					if k == "trQuality" && v != "high" {
						v = "fast"
					}
					a.hub.h.Settings[k] = v
				}
			}
			_ = a.hub.saveLocked()
			a.hub.mu.Unlock()
		}
		out := map[string]string{}
		for _, k := range profileKeys {
			out[k] = a.setting(k)
		}
		writeJSON(w, out)
	}))
}
