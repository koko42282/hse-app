package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
)

// The STOP-card database shipped with the program: cleaned descriptions + classifications from the user's
// past Sonatrach logs, the SP236 register and the GW112 tracking register. No names of observers or
// responsible persons are included. Merged with duplicates skipped; cards stay "not verified" until reviewed.
//
//go:embed data/stopseed.json.gz
var stopSeedGz []byte

// Operation-specific STOP cards written for well operations (logging, cementing, DST, coil tubing ...).
// Marked source "generic" (generated, not real observations) and loaded once, separately from the main seed.
//
//go:embed data/stopseed_ops.json.gz
var stopOpsGz []byte

func (h *hubStore) importOps() (int, int) {
	zr, err := gzip.NewReader(bytes.NewReader(stopOpsGz))
	if err != nil {
		return 0, 0
	}
	b, _ := io.ReadAll(zr)
	var cards []StopCard
	if json.Unmarshal(b, &cards) != nil {
		return 0, 0
	}
	add, dup := h.addStops(cards)
	h.mu.Lock()
	h.ver++
	h.h.Settings["stopOpsDone"] = "1"
	_ = h.saveLocked()
	h.mu.Unlock()
	return add, dup
}

func (h *hubStore) importSeed() (int, int) {
	zr, err := gzip.NewReader(bytes.NewReader(stopSeedGz))
	if err != nil {
		return 0, 0
	}
	b, _ := io.ReadAll(zr)
	var cards []StopCard
	if json.Unmarshal(b, &cards) != nil {
		return 0, 0
	}
	add, dup := h.addStops(cards)
	h.mu.Lock()
	h.ver++
	h.h.Settings["stopSeedDone"] = "1"
	_ = h.saveLocked()
	h.mu.Unlock()
	return add, dup
}

func (a *app) stopSeedHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/stopdb/seed", guard(func(w http.ResponseWriter, r *http.Request) {
		add, dup := a.hub.importSeed()
		a2, d2 := a.hub.importOps()
		add, dup = add+a2, dup+d2
		writeJSON(w, map[string]interface{}{"ok": true, "added": add, "dup": dup})
	}))
}
