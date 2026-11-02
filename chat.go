package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	ragTopK        = 3
	ragCtxChars    = 2600
	maxAnswerToken = 600
)

const systemPromptRAG = `أنت مساعد متخصص في السلامة والصحة المهنية والبيئة (HSE).
أجب فقط اعتماداً على «المراجع» المرفقة مع السؤال. إن لم يكن الجواب موجوداً فيها فقل بوضوح: «لا أجد ذلك في المراجع المتوفرة» ولا تخمّن.
لا تخترع أرقاماً أو حدوداً أو معايير أو أسماء. انقل الأرقام كما وردت في المراجع.
المراجع قد تكون بالعربية أو الإنجليزية أو الفرنسية أو الصينية. أجب بلغة السؤال (用中文提问就用中文回答)، بإيجاز ودقة، واذكر رقم المرجع بين قوسين مثل [1].
You are an HSE (health, safety, environment) assistant. Answer ONLY from the supplied references; if the answer is not there, say so. Never invent numbers or standards. Cite references like [1]. When asked for a report, table or list, write it in Markdown (# headings, - bullets, | tables |).
For Excel requests: write a Markdown table (first row = headers); put formulas in cells like =SUM(B2:B5) or =B2*C2 as if the table started at cell A1; for a chart add a separate line: [chart: bar | Title | cats=A | values=B,C] (types: bar, line, pie). المعادلات في الخلايا تبدأ بـ = والرسم بسطر [chart: ...].`

const systemPromptPlain = `أنت مساعد متخصص في السلامة والصحة المهنية والبيئة (HSE). أجب بإيجاز ودقة وبلغة السؤال، وصرّح بعدم اليقين عند الحاجة، ولا تخترع أرقاماً أو معايير. عند طلب تقرير أو جدول اكتبه بتنسيق Markdown (عناوين # وقوائم - وجداول |).
For Excel requests: write a Markdown table (first row = headers); put formulas in cells like =SUM(B2:B5) or =B2*C2 as if the table started at cell A1; for a chart add a separate line: [chart: bar | Title | cats=A | values=B,C] (types: bar, line, pie). المعادلات في الخلايا تبدأ بـ = والرسم بسطر [chart: ...].`

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Message string    `json:"message"`
	History []chatMsg `json:"history"`
	RAG     bool      `json:"rag"`
	Strict  bool      `json:"strict"`
}

type srcItem struct {
	N      int      `json:"n"`
	File   string   `json:"file"`
	Page   int      `json:"page"`
	Text   string   `json:"text"`
	Imgs   []string `json:"imgs,omitempty"`
	IsPDF  bool     `json:"pdf,omitempty"`
	PdfImg bool     `json:"pdfimg,omitempty"`
}

func mediaURL(rel, entry string) string {
	if entry == "@self" {
		return "/api/rag/file?rel=" + url.QueryEscape(rel)
	}
	return "/api/rag/media?rel=" + url.QueryEscape(rel) + "&n=" + url.QueryEscape(entry)
}

func truncRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// thinkFilter removes <think>...</think> blocks from a token stream.
type thinkFilter struct {
	in  bool
	buf string
}

func (f *thinkFilter) feed(s string) string {
	f.buf += s
	var out strings.Builder
	for {
		if f.in {
			i := strings.Index(f.buf, "</think>")
			if i < 0 {
				if len(f.buf) > 8 {
					f.buf = f.buf[len(f.buf)-8:]
				}
				return out.String()
			}
			f.buf = f.buf[i+len("</think>"):]
			f.in = false
			continue
		}
		i := strings.Index(f.buf, "<think>")
		if i >= 0 {
			out.WriteString(f.buf[:i])
			f.buf = f.buf[i+len("<think>"):]
			f.in = true
			continue
		}
		// keep a possible partial "<think>" tail
		keep := 0
		for k := 1; k < len("<think>") && k <= len(f.buf); k++ {
			if strings.HasPrefix("<think>", f.buf[len(f.buf)-k:]) {
				keep = k
			}
		}
		out.WriteString(f.buf[:len(f.buf)-keep])
		f.buf = f.buf[len(f.buf)-keep:]
		return out.String()
	}
}

func (f *thinkFilter) flush() string {
	if f.in {
		return ""
	}
	s := f.buf
	f.buf = ""
	return s
}

func (a *app) handleChat(w http.ResponseWriter, r *http.Request) {
	var in chatReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || strings.TrimSpace(in.Message) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	state, port, noThink := a.state, a.port, a.noThink
	a.mu.Unlock()
	if state != "running" {
		http.Error(w, "model is not running", http.StatusConflict)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	send := func(v map[string]interface{}) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	start := time.Now()
	question := strings.TrimSpace(in.Message)
	messages := []map[string]string{}
	var userContent string

	if in.RAG {
		query := question
		if len(tokenize(question)) <= 3 {
			for i := len(in.History) - 1; i >= 0; i-- {
				if in.History[i].Role == "user" {
					query = in.History[i].Content + " " + question
					break
				}
			}
		}
		type mhit struct {
			si     *searchIndex
			prefix string
			h      hit
		}
		sis := []struct {
			si     *searchIndex
			prefix string
		}{{a.rag.searcher(), ""}}
		if ksi, _ := a.kbSearcher(); ksi != nil {
			sis = append(sis, struct {
				si     *searchIndex
				prefix string
			}{ksi, "KB/"})
		}
		if a.cat != nil {
			sis = append(sis, struct {
				si     *searchIndex
				prefix string
			}{a.cat.searcher(), catPrefix})
		}
		searchAll := func(q string) []mhit {
			var all []mhit
			for _, x := range sis {
				if x.si == nil {
					continue
				}
				for _, h := range x.si.search(q, ragTopK) {
					all = append(all, mhit{x.si, x.prefix, h})
				}
			}
			sort.SliceStable(all, func(i, j int) bool { return all[i].h.Score > all[j].h.Score })
			if len(all) > ragTopK {
				all = all[:ragTopK]
			}
			return all
		}
		noDocs := sis[0].si == nil && (a.cat == nil || a.cat.searcher() == nil)
		anyIdx := true
		if noDocs {
			st := a.rag.status()
			msg := "⚠ لا توجد ملفات في rag أو Catalogs؛ لن يُستعمل سوى قاعدة معرفة HSE المدمجة ومعرفة النموذج. مجلد المراجع المستعمل: " + st.Dir
			if !st.Exists {
				msg += " (المجلد غير موجود — يجب أن يسمى rag بجانب البرنامج)"
			} else if st.Indexing {
				msg += " (الفهرسة جارية، أعد السؤال بعد قليل)"
			} else {
				msg += " (اضغط على سطر 📚 ثم «تحديث الفهرس الآن»)"
			}
			send(map[string]interface{}{"type": "notice", "v": msg})
		}
		var hits []mhit
		if anyIdx {
			hits = searchAll(query)
			if len(hits) == 0 {
				// the references may be in another language: translate the question into search keywords
				send(map[string]interface{}{"type": "notice", "v": "🔄 لم أجد في لغة السؤال؛ أترجم السؤال للبحث في مراجع بلغات أخرى..."})
				if tr, err := a.translateQuery(r.Context(), port, noThink, question); err == nil && tr != "" {
					hits = searchAll(tr)
					if len(hits) == 0 {
						hits = searchAll(query + " " + tr)
					}
				}
			}
		}
		var srcs []srcItem
		var ctx strings.Builder
		budget := ragCtxChars
		for i, h := range hits {
			c := h.si.data.Chunks[h.h.Chunk]
			f := h.si.data.Files[c.File]
			f.Rel = h.prefix + f.Rel
			text := truncRunes(c.Text, budget)
			budget -= len([]rune(text))
			it := srcItem{N: i + 1, File: f.Rel, Page: c.Page, Text: c.Text, IsPDF: strings.HasSuffix(strings.ToLower(f.Rel), ".pdf") && c.Page > 0, PdfImg: c.PdfImg}
			for _, e := range c.Img {
				it.Imgs = append(it.Imgs, mediaURL(f.Rel, e))
			}
			srcs = append(srcs, it)
			loc := f.Rel
			if c.Page > 0 {
				loc += " p." + strconv.Itoa(c.Page)
			}
			fmt.Fprintf(&ctx, "[%d] (%s)\n%s\n", i+1, loc, text)
			if len(it.Imgs) > 0 || it.PdfImg {
				ctx.WriteString("(ملاحظة: يعرض البرنامج للمستخدم صورة/شكلاً من هذا المرجع؛ يمكنك الإحالة إليه.)\n")
			}
			ctx.WriteString("\n")
			if budget < 120 {
				break
			}
		}
		send(map[string]interface{}{"type": "sources", "items": srcs, "ms": time.Since(start).Milliseconds()})
		if len(srcs) == 0 {
			if in.Strict {
				send(map[string]interface{}{"type": "token", "v": "لا أجد في المراجع المتوفرة (مجلدا rag و Catalogs) ما يتعلق بسؤالك. جرّب صياغة أخرى أو أضف المرجع المناسب."})
				send(map[string]interface{}{"type": "done", "ms": time.Since(start).Milliseconds(), "tokens": 0})
				return
			}
			send(map[string]interface{}{"type": "notice", "v": "لم أجد مراجع مناسبة؛ الجواب من معرفة النموذج العامة وقد لا يكون دقيقاً."})
			messages = append(messages, map[string]string{"role": "system", "content": systemPromptPlain})
			userContent = question
		} else {
			messages = append(messages, map[string]string{"role": "system", "content": systemPromptRAG})
			userContent = "المراجع:\n" + ctx.String() + "السؤال: " + question
		}
	} else {
		messages = append(messages, map[string]string{"role": "system", "content": systemPromptPlain})
		userContent = question
	}
	// short history (last 2 exchanges, trimmed) for follow-up questions
	h := in.History
	if len(h) > 4 {
		h = h[len(h)-4:]
	}
	for _, m := range h {
		if (m.Role == "user" || m.Role == "assistant") && strings.TrimSpace(m.Content) != "" {
			messages = append(messages, map[string]string{"role": m.Role, "content": truncRunes(m.Content, 500)})
		}
	}
	userContent += "\n\n" + answerLangNote(question)
	messages = append(messages, map[string]string{"role": "user", "content": userContent})

	body := map[string]interface{}{
		"model": "local", "messages": messages, "stream": true, "cache_prompt": true,
		"temperature": 0.2, "top_p": 0.9, "max_tokens": maxAnswerToken,
	}
	if noThink {
		body["chat_template_kwargs"] = map[string]interface{}{"enable_thinking": false}
	}
	bb, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(r.Context(), "POST", "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/chat/completions", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 15 * time.Minute}}
	resp, err := client.Do(req)
	if err != nil {
		send(map[string]interface{}{"type": "error", "v": "تعذّر الاتصال بالنموذج: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		send(map[string]interface{}{"type": "error", "v": "خطأ من النموذج: HTTP " + strconv.Itoa(resp.StatusCode)})
		return
	}
	var tf thinkFilter
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	tokens, first := 0, time.Duration(0)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil || len(ev.Choices) == 0 {
			continue
		}
		if v := tf.feed(ev.Choices[0].Delta.Content); v != "" {
			if first == 0 {
				first = time.Since(start)
			}
			tokens++
			send(map[string]interface{}{"type": "token", "v": v})
		}
	}
	if v := tf.flush(); v != "" {
		send(map[string]interface{}{"type": "token", "v": v})
	}
	send(map[string]interface{}{"type": "done", "ms": time.Since(start).Milliseconds(), "ttft": first.Milliseconds(), "tokens": tokens})
}

// answerLangNote tells the model which language to answer in (the question's), even if the references differ.
func answerLangNote(q string) string {
	ar, han := 0, 0
	for _, r := range q {
		if isArabicRune(r) {
			ar++
		} else if unicode.Is(unicode.Han, r) {
			han++
		}
	}
	switch {
	case han > 0 && han >= ar:
		return "请用中文回答；如果参考资料是其他语言，请翻译其内容。"
	case ar > 0:
		return "أجب بالعربية فقط حتى لو كانت المراجع بالإنجليزية أو الفرنسية أو الصينية؛ ترجم المعلومات المطلوبة إلى العربية وأبقِ الأرقام والرموز والأسماء التقنية كما وردت."
	}
	return "Answer in the language of the question, even if the references are in another language (translate the information)."
}

// translateQuery asks the local model for English/French search keywords for the question.
func (a *app) translateQuery(ctx context.Context, port int, noThink bool, q string) (string, error) {
	body := map[string]interface{}{
		"model": "local", "stream": false, "cache_prompt": true, "temperature": 0, "max_tokens": 70,
		"messages": []map[string]string{
			{"role": "system", "content": "You convert a health-safety-environment (HSE) question into search keywords. Output ONE line only: English keywords, then ' | ', then French keywords. At most 10 words in each part. No explanations."},
			{"role": "user", "content": truncRunes(q, 300)},
		},
	}
	if noThink {
		body["chat_template_kwargs"] = map[string]interface{}{"enable_thinking": false}
	}
	bb, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/chat/completions", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var o struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(b, &o) != nil || len(o.Choices) == 0 {
		return "", fmt.Errorf("empty")
	}
	var tf thinkFilter
	txt := tf.feed(o.Choices[0].Message.Content) + tf.flush()
	txt = strings.NewReplacer("|", " ", "\n", " ", "English:", " ", "French:", " ", "Keywords:", " ").Replace(txt)
	return strings.TrimSpace(txt), nil
}
