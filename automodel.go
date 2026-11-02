package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ensureModel starts Qwen3-1.7B-Q4_K_M automatically (thinking off) when an AI feature needs the model, and waits until it is ready.
// errModelOff: the model is stopped and the user has not approved starting it; the UI shows a confirmation and retries.
var errModelOff = errors.New("MODEL_OFF")

func (a *app) ensureModel() error {
	if a.modelRunning() {
		return nil
	}
	a.mu.Lock()
	st := a.state
	if st != "loading" && !a.consent {
		a.mu.Unlock()
		return errModelOff
	}
	if len(a.models) == 0 {
		a.scan()
	}
	idx := -1
	if pref := a.setting("model_pref"); pref != "" { // the user's chosen model (System tab)
		for i, m := range a.models {
			if m.Name == pref {
				idx = i
			}
		}
	}
	for i, m := range a.models {
		if idx >= 0 {
			break
		}
		n := strings.ToLower(m.Name + " " + m.Path)
		if strings.Contains(n, "qwen3-1.7b") && strings.Contains(n, "q4_k_m") {
			idx = i
			break
		}
	}
	if idx < 0 {
		for i, m := range a.models {
			if strings.Contains(strings.ToLower(m.Name+m.Path), "qwen3-1.7b") {
				idx = i
				break
			}
		}
	}
	if idx < 0 && len(a.models) == 1 {
		idx = 0
	}
	a.mu.Unlock()
	if st != "loading" {
		if idx < 0 {
			return fmt.Errorf("Qwen3-1.7B-Q4_K_M غير موجود في AI_CORE")
		}
		if err := a.startModel(idx, true); err != nil && !strings.Contains(err.Error(), "already") {
			return err
		}
	}
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		s, em := a.state, a.errMsg
		a.mu.Unlock()
		switch s {
		case "running":
			return nil
		case "error":
			return fmt.Errorf("تعذّر تشغيل النموذج: %s", em)
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return fmt.Errorf("انتهت مهلة تشغيل النموذج")
}

// modelOffNow: stopped and not approved by the user (the UI should ask before the call is retried)
func (a *app) modelOffNow() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state != "running" && a.state != "loading" && !a.consent
}
