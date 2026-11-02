package main

// A small linear text classifier (hashed word 1-2-grams + character 3-4-grams, softmax regression trained
// by SGD). It replaces naive Bayes for STOP cards: on the user's own cards it is several points more accurate,
// trains in about a second and keeps the model under 2 MB per label set, so it runs fine on an i3 / 4 GB PC.

import (
	"hash/fnv"
	"math"
	"sort"
)

const linDim = 1 << 17

type linModel struct {
	labels []string
	w      [][]float32
	b      []float32
	n      int
	nb     *nbModel // fallback while there are too few cards
}

func linFeats(toks []string) map[uint32]float32 {
	f := map[uint32]float32{}
	add := func(s string) {
		h := fnv.New32a()
		h.Write([]byte(s))
		f[h.Sum32()%linDim]++
	}
	for i, t := range toks {
		add("w:" + t)
		if i+1 < len(toks) {
			add("b:" + t + " " + toks[i+1])
		}
		r := []rune("<" + t + ">")
		for n := 3; n <= 4; n++ {
			for j := 0; j+n <= len(r); j++ {
				add("c:" + string(r[j:j+n]))
			}
		}
	}
	var z float64
	for k, v := range f {
		v = float32(1 + math.Log(float64(v)))
		f[k] = v
		z += float64(v * v)
	}
	if z > 0 {
		z = math.Sqrt(z)
		for k := range f {
			f[k] = float32(float64(f[k]) / z)
		}
	}
	return f
}

func trainLin(docs [][]string, labels []string) *linModel {
	m := &linModel{}
	idx := map[string]int{}
	for _, l := range labels {
		if l == "" {
			continue
		}
		if _, ok := idx[l]; !ok {
			idx[l] = len(m.labels)
			m.labels = append(m.labels, l)
		}
		m.n++
	}
	if m.n < 40 || len(m.labels) < 2 {
		m.nb = trainNB(docs, labels)
		return m
	}
	K := len(m.labels)
	m.w = make([][]float32, K)
	for k := range m.w {
		m.w[k] = make([]float32, linDim)
	}
	m.b = make([]float32, K)
	type ex struct {
		f map[uint32]float32
		y int
	}
	var data []ex
	for i, d := range docs {
		if labels[i] == "" {
			continue
		}
		data = append(data, ex{linFeats(d), idx[labels[i]]})
	}
	order := make([]int, len(data))
	for i := range order {
		order[i] = i
	}
	seed := uint32(12345)
	rnd := func() uint32 { seed = seed*1664525 + 1013904223; return seed }
	p := make([]float64, K)
	for ep := 0; ep < 12; ep++ {
		for i := len(order) - 1; i > 0; i-- {
			j := int(rnd() % uint32(i+1))
			order[i], order[j] = order[j], order[i]
		}
		lr := float32(0.8 / (1 + 0.35*float64(ep)))
		for _, oi := range order {
			e := data[oi]
			mx := -1e30
			for k := 0; k < K; k++ {
				s := float64(m.b[k])
				for h, v := range e.f {
					s += float64(m.w[k][h] * v)
				}
				p[k] = s
				if s > mx {
					mx = s
				}
			}
			var z float64
			for k := 0; k < K; k++ {
				p[k] = math.Exp(p[k] - mx)
				z += p[k]
			}
			for k := 0; k < K; k++ {
				g := float32(p[k] / z)
				if k == e.y {
					g -= 1
				}
				g *= lr
				m.b[k] -= g
				for h, v := range e.f {
					m.w[k][h] -= g*v + lr*1e-5*m.w[k][h]
				}
			}
		}
	}
	return m
}

func (m *linModel) predict(toks []string) (string, float64) {
	if m == nil {
		return "", 0
	}
	if m.nb != nil || m.w == nil {
		return m.nb.predict(toks)
	}
	f := linFeats(toks)
	K := len(m.labels)
	s := make([]float64, K)
	mx := -1e30
	for k := 0; k < K; k++ {
		v := float64(m.b[k])
		for h, x := range f {
			v += float64(m.w[k][h] * x)
		}
		s[k] = v
		if v > mx {
			mx = v
		}
	}
	var z float64
	for k := range s {
		s[k] = math.Exp(s[k] - mx)
		z += s[k]
	}
	order := make([]int, K)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return s[order[a]] > s[order[b]] })
	return m.labels[order[0]], s[order[0]] / z
}
