package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ---- Intent assistant: understands a request (ar/fr/en) and proposes actions.
// Rules first (reliable on a weak model); anything that changes data returns a proposal the user must confirm.

var arNorm = strings.NewReplacer("أ", "ا", "إ", "ا", "آ", "ا", "ى", "ي", "ة", "ه", "ـ", "", "ً", "", "ٌ", "", "ٍ", "", "َ", "", "ُ", "", "ِ", "", "ّ", "", "ْ", "", "é", "e", "è", "e", "ê", "e", "à", "a", "ç", "c", "ô", "o", "î", "i", "'", " ", "’", " ", "-", " ")

func normQ(s string) string {
	return strings.Join(strings.Fields(arNorm.Replace(strings.ToLower(s))), " ")
}

func hasAny(s string, ws ...string) bool {
	for _, w := range ws {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

type navT struct {
	tab, sub string
	kw       []string
}

var assistNav = []navT{
	{"kb", "learn", []string{"تعلم", "مسار التعلم", "learning path", "learn hse", "apprendre", "parcours"}},
	{"kb", "assist", []string{"المساعد", "assistant"}},
	{"people", "ind", []string{"induction", "استقبال"}},
	{"people", "pt", []string{"pointage", "الحضور", "timesheet", "pointeuse"}},
	{"people", "pob", []string{"pob", "الافراد", "افراد", "personnel", "people"}},
	{"ptw", "", []string{"ptw", "تصريح", "تصاريح", "permis", "permit"}},
	{"stop", "", []string{"stop", "ستوب"}},
	{"training", "", []string{"تدريب", "training", "formation"}},
	{"drills", "", []string{"تمرين", "تمارين", "drill", "exercice"}},
	{"inspect", "", []string{"تفتيش", "inspection"}},
	{"incident", "", []string{"حادث", "حوادث", "incident", "accident"}},
	{"green", "", []string{"green day", "اخضر", "vert"}},
	{"report", "", []string{"تقرير", "تقارير", "report", "rapport"}},
	{"kb", "kb", []string{"معرفه", "knowledge", "savoir", "connaissance", "قاعده"}},
	{"ocr", "", []string{"ocr"}},
	{"catalogs", "", []string{"catalog", "كتالوج", "فهرس"}},
	{"system", "", []string{"النظام", "system", "systeme", "اعدادات", "settings", "parametre"}},
	{"today", "", []string{"اليوم الرئيسي", "home", "accueil", "الرئيسيه"}},
}

var tabNameAR = map[string]string{"kb": "معرفة HSE", "people": "الأفراد", "ptw": "تصاريح PTW", "stop": "STOP", "training": "التدريب", "drills": "التمارين", "inspect": "التفتيش", "incident": "الحوادث", "green": "Green Day", "report": "التقارير", "ocr": "OCR", "catalogs": "Catalogs", "system": "النظام", "today": "اليوم"}

var (
	reOpen   = []string{"افتح", "اذهب", "خذني", "اعرض", "ورني", "open", "go to", "show me", "show", "take me", "ouvre", "ouvrir", "aller", "affiche", "montre"}
	reAdd    = []string{"اضف", "سجل", "انشئ", "اضافه", "add", "create", "new", "log", "record", "ajoute", "creer", "nouvelle", "enregistre"}
	reCount  = []string{"كم", "عدد", "combien", "how many", "count", "nombre", "total"}
	reDefine = []string{"ما هو", "ما هي", "ما معني", "معني", "ماذا يعني", "عرف", "اشرح", "شرح", "what is", "what are", "what s", "what does", "define", "meaning", "explain", "c est quoi", "qu est ce", "que veut dire", "definition", "explique", "signifie"}
	reHow    = []string{"كيف", "طريقه", "خطوات", "how to", "how do", "how can", "steps", "comment", "etapes"}
	reCheck  = []string{"قائمه فحص", "قائمه تحقق", "checklist", "check list", "liste de controle", "liste de verification", "فحص"}
	reBuild  = []string{"ابن", "اعمل", "حضر", "جهز", "اصنع", "انشئ", "build", "generate", "prepare", "make", "genere", "prepare", "construis", "fais"}
	reTrans  = []string{"ترجم", "translate", "traduis", "traduire"}
	reTeach  = []string{"علمني", "درسني", "teach me", "apprends moi", "enseigne moi", "start learning", "ابدا التعلم", "i am new", "انا جديد", "je suis nouveau", "debutant"}
	reHelp   = []string{"ماذا تستطيع", "ماذا تفعل", "مساعده", "help", "what can you", "aide", "que peux"}
)

func (a *app) assistCounts() (pob, day, night, openPTW int, late []string) {
	a.hub.mu.Lock()
	for _, p := range a.hub.h.People {
		if p.onBoardNow() {
			pob++
			if p.Shift == "Night" {
				night++
			} else {
				day++
			}
		}
	}
	a.hub.mu.Unlock()
	for _, r := range a.hub.recsOf("ptw") {
		st := strings.ToLower(r.S("status"))
		if st == "closed" || st == "مغلق" {
			continue
		}
		openPTW++
	}
	_, late = a.openPTWReminders(time.Now().Format("2006-01-02"))
	return
}

var reColon = regexp.MustCompile(`[:：]\s*(.+)$`)

// strip leading command words so the rest is the subject
var subjNoise = []string{"what should i know about", "tell me about", "parle moi de", "parlez moi de", "dis moi", "كيف اتعامل مع", "ماذا افعل بخصوص", "ما خطر", "how do i stay safe around", "how to stay safe around", "stay safe around", "et quels dangers", "quels sont les dangers de", "quels dangers", "and how do we control them", "the hazards of", "hazards of", "dangers of", "وكيف نتحكم بها", "كيف نتحكم بها", "كيف اراقب وافتش", "كيف افتش", "علي الحفاره", "على الحفاره", "اخطار", "مخاطر"}

func subject(raw, nq string, verbs ...[]string) string {
	if m := reColon.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(m[1])
	}
	s := nq
	for _, vs := range verbs {
		for _, v := range vs {
			s = strings.Replace(s, v, " ", 1)
		}
	}
	s = strings.NewReplacer("?", " ", "؟", " ", "!", " ", ".", " ", ",", " ", "،", " ").Replace(s)
	for _, n := range subjNoise {
		s = strings.Replace(s, n, " ", -1)
	}
	f := strings.Fields(s)
	for len(f) > 1 && hasAny(" a an the le la les l un une de du des of to que qu est ce is are ", " "+f[0]+" ") {
		f = f[1:]
	}
	s = strings.Join(f, " ")
	return strings.Join(strings.Fields(s), " ")
}

func (a *app) assistRun(text string) map[string]interface{} {
	raw := strings.TrimSpace(text)
	nq := normQ(raw)
	out := map[string]interface{}{"ok": true}
	acts := []map[string]interface{}{}
	reply := ""
	today := time.Now().Format("2006-01-02")
	done := func() map[string]interface{} {
		out["reply"], out["actions"] = reply, acts
		return out
	}
	if nq == "" {
		reply = "اكتب طلبك، مثلاً: «ما هو LOTO؟» · «كم POB؟» · «افتح تصاريح PTW» · «أضف بطاقة STOP: عامل بدون خوذة»"
		return done()
	}
	if a.assistExact(raw, nq, &reply, &acts) {
		return done()
	}
	if !hasAny(nq, "ستوب", "stop card") && hasAny(nq, "اسمنت", "تسمنت", "سمنت", "cementing", "تثقيب", "perforat", "كويل", "coil tubing", "slickline", "wireline", "سليك", "وايرلاين", "اختبار التكوين", "dst", "الاختبار السطحي", "تحفيز", "نيتروجين", "علوق", "صيد", "fishing", "workover", "كورينغ", "coring", "مادلوغ", "mud logging", "نقل الحفاره", "rig move", "تنزيل الكيسنغ", "running casing", "simops", "تزامن", "متزامن", "تصحيحي", "corrective action") && a.assistGuide(raw, nq, &reply, &acts) {
		acts = append(acts, map[string]interface{}{"type": "chat", "q": raw, "label": "💬 اسأل الدردشة"})
		return done()
	}
	isStop := hasAny(nq, "stop", "ستوب") && hasAny(nq, "بطاقه", "card", "carte", "ملاحظه", "observation")
	switch {
	// 1) add a STOP card (confirm first)
	case isStop && hasAny(nq, reAdd...):
		desc := subject(raw, nq, reAdd, []string{"stop", "ستوب", "بطاقه", "card", "carte", "ملاحظه"})
		if len([]rune(desc)) < 6 {
			reply = "اكتب وصف الملاحظة بعد النقطتين، مثلاً: «أضف بطاقة STOP: عامل يعمل بدون قفازات في أرضية الحفر»"
			break
		}
		c := StopCard{Date: today, Desc: desc, Observer: a.setting("hseName"), Function: "HSE", Zone: "", Status: "Open"}
		n, cl, rk, _ := a.hub.classifier().classify(c)
		c.Nature, c.Class, c.Risk = n, cl, rk
		// the learned classifier is weak on short texts: obvious negatives / PPE words override it
		if hasAny(nq, "بدون", "without", "sans ", "لا يرتدي", "not wearing", "no ", "مكشوف", "تسرب", "leak", "missing", "مفقود", "fuite") {
			c.Nature = "Unsafe Act"
			if hasAny(nq, "تسرب", "leak", "fuite", "مكشوف", "missing", "مفقود") {
				c.Nature = "Unsafe Condition"
			}
		}
		if hasAny(nq, "خوذ", "قفاز", "حذاء", "نظارات", "حزام", "helmet", "glove", "boot", "goggle", "harness", "epi", "ppe", "casque", "gant") {
			c.Class = "EPI"
		}
		reply = "جهّزتُ بطاقة STOP مبدئية. راجع الحقول ثم أكّد الحفظ (التصنيف اقتراح آلي)."
		acts = append(acts, map[string]interface{}{"type": "stop", "card": c})
	// 2) counts
	case hasAny(nq, "pob", "افراد", "personnel", "people") && hasAny(nq, reCount...):
		pob, d, n, op, _ := a.assistCounts()
		reply = fmt.Sprintf("POB الحالي: %d شخصاً (نهار %d · ليل %d). تصاريح PTW غير المغلقة: %d.", pob, d, n, op)
		acts = append(acts, map[string]interface{}{"type": "nav", "tab": "people", "sub": "pob", "label": "افتح POB"})
	case hasAny(nq, "ptw", "تصريح", "تصاريح", "permis", "permit") && hasAny(nq, reCount...) && !hasAny(nq, "خطر", "اخطار", "hazard", "danger") || hasAny(nq, "مفتوح", "open permit", "permis ouvert"):
		_, _, _, op, late := a.assistCounts()
		reply = fmt.Sprintf("تصاريح PTW غير المغلقة: %d.", op)
		if len(late) > 0 {
			reply += " ⚠ متأخرة عن وقتها: " + strings.Join(late, "، ")
		}
		acts = append(acts, map[string]interface{}{"type": "nav", "tab": "ptw", "label": "افتح سجل PTW"})
	// 3) teacher
	case hasAny(nq, reTeach...):
		reply = "ابدأ من مسار «تعلّم HSE»: دروس مرتبة مع اختبارات قصيرة وتتبّع تقدّمك."
		acts = append(acts, map[string]interface{}{"type": "nav", "tab": "kb", "sub": "learn", "label": "ابدأ التعلم"})
	case hasAny(nq, reHelp...):
		reply = "أستطيع: فتح أي تبويب · شرح مصطلح (ما هو LOTO؟) · البحث في المعرفة · عرض قوائم الفحص وطريقة العمل · عدّ POB وتصاريح PTW · تجهيز بطاقة STOP · ترجمة نص · تجهيز التقرير اليومي · وللأسئلة المفتوحة الدردشة مع النموذج."
	// 4) translate
	case hasAny(nq, reTrans...):
		t := subject(raw, nq, reTrans)
		to := "en"
		if hasAny(nq, "عربي", "arabic", "arabe") {
			to = "ar"
		} else if hasAny(nq, "فرنس", "french", "francais") {
			to = "fr"
		}
		if m := reColon.FindStringSubmatch(raw); m != nil {
			t = m[1]
		}
		reply = "سأترجم النص (يحتاج النموذج، والنتيجة للمراجعة)."
		acts = append(acts, map[string]interface{}{"type": "translate", "text": t, "to": to})
	// 5) build the daily report
	case hasAny(nq, reBuild...) && hasAny(nq, "تقرير", "report", "rapport"):
		reply = "افتح صفحة التقرير، اختر التاريخ واللغة ثم اضغط «بناء». سأفتحها لك."
		acts = append(acts, map[string]interface{}{"type": "nav", "tab": "report", "label": "تقرير صوناطراك"})
	// 6) definitions -> glossary first
	case hasAny(nq, reDefine...):
		q := subject(raw, nq, reDefine)
		if (hasAny(nq, reHow...) || a.tagPin(q, nq)) && a.assistGuide(q, nq, &reply, &acts) {
			break
		}
		a.assistKB(q, "glossary", 3, &reply, &acts, true)
		if len(acts) == 0 {
			a.assistKB(q, "", 4, &reply, &acts, true)
		}
	// 7) checklists
	case hasAny(nq, reCheck...) && !hasAny(nq, reOpen...):
		q := subject(raw, nq, reCheck)
		if a.assistGuide(q, nq, &reply, &acts) {
			break
		}
		a.assistKB(q, "inspect", 4, &reply, &acts, false)
		if len(acts) == 0 {
			a.assistKB(q, "", 4, &reply, &acts, false)
		}
	// 8) how-to
	case hasAny(nq, reHow...):
		q := subject(raw, nq, reHow)
		if a.assistGuide(q, nq, &reply, &acts) {
			break
		}
		a.assistKB(q, "", 5, &reply, &acts, false)
	// 9) navigation
	case hasAny(nq, reOpen...):
		for _, n := range assistNav {
			if hasAny(nq, n.kw...) {
				reply = "حاضر، أفتح «" + tabNameAR[n.tab] + "»."
				acts = append(acts, map[string]interface{}{"type": "nav", "tab": n.tab, "sub": n.sub, "label": "افتح " + tabNameAR[n.tab], "auto": true})
				break
			}
		}
	}
	if reply == "" && len(acts) == 0 {
		// fallback: knowledge search, else hand over to the chat
		a.assistKB(raw, "", 5, &reply, &acts, false)
	}
	if len(acts) == 0 {
		reply = "لم أجد جواباً مباشراً في قاعدة المعرفة. يمكنك سؤال الدردشة (النموذج + ملفاتك)."
	}
	acts = append(acts, map[string]interface{}{"type": "chat", "q": raw, "label": "💬 اسأل الدردشة"})
	return done()
}

// kb lookups: top hits as actions; when explain=true the first answer is quoted in the reply
func (a *app) assistKB(q, cat string, n int, reply *string, acts *[]map[string]interface{}, explain bool) {
	if strings.TrimSpace(q) == "" {
		return
	}
	si, es := a.kbSearcher()
	var got, hits []kbEntry
	for _, h := range si.search(q, 60) {
		e := es[si.data.Chunks[h.Chunk].File]
		if cat != "" && e.Cat != cat || cat == "" && !explain && e.Cat == "glossary" {
			continue
		}
		hits = append(hits, e)
	}
	// entries whose title / tags contain the whole subject come first
	nq := normQ(q)
	rank := func(e kbEntry) int {
		r := 2
		for _, t := range []string{e.Title["ar"], e.Title["fr"], e.Title["en"]} {
			nt := normQ(t)
			if nt == "" {
				continue
			}
			base := strings.TrimSpace(strings.SplitN(nt, "(", 2)[0])
			if nt == nq || base == nq {
				return 0
			}
			if strings.Contains(nt, nq) || len(nq) > 2 && strings.Contains(nq, nt) {
				r = 1
			}
		}
		for _, t := range e.Tags {
			if normQ(t) == nq {
				return 0
			}
		}
		return r
	}
	for pass := 0; pass < 3; pass++ {
		for _, e := range hits {
			if rank(e) == pass && len(got) < n {
				got = append(got, e)
			}
		}
	}
	if len(got) == 0 {
		return
	}
	if explain {
		e := got[0]
		b := firstOf(e.Body["ar"], e.Body["fr"], e.Body["en"])
		*reply = "📖 " + firstOf(e.Title["ar"], e.Title["en"]) + " (" + firstOf(e.Title["en"], e.Title["fr"]) + "): " + b
	} else {
		*reply = fmt.Sprintf("وجدتُ %d مادة مناسبة في قاعدة المعرفة:", len(got))
	}
	for _, e := range got {
		*acts = append(*acts, map[string]interface{}{"type": "kb", "id": e.ID, "title": e.Title, "kind": e.Type, "label": "📖 " + firstOf(e.Title["ar"], e.Title["en"])})
	}
}


// chkKW maps spoken words (ar / fr / en, normalised) to checklist template ids
var chkKW = map[string][]string{
	"kitchen":       {"مطبخ", "kitchen", "cuisine", "طعام", "dining", "refectoire", "fifo", "lifo", "fefo"},
	"camp":          {"مخيم", "camp", "حياه", "accommodation"},
	"forklift":      {"شوكيه", "forklift", "chariot", "fork lift"},
	"crane":         {"crane", "grue", "رافعه ثقيله", "رافعه crane", "كرين"},
	"rigsafety":     {"سلامه الحفاره", "rig safety", "securite du rig", "rig inspection", "فحص الحفاره"},
	"wellcontrol":   {"التحكم في البئر", "well control", "controle de puits", "bop", "accumulator", "اكيوميوليتر"},
	"scba":          {"scba", "جهاز تنفس", "اجهزه التنفس", "h2s equipment", "ari "},
	"grounding":     {"تاريض", "grounding", "earthing", "mise a la terre"},
	"dropprev":      {"سقوط الاجسام", "drop prevention", "dropped object", "chute d'objets"},
	"clinic":        {"عياده", "clinic", "clinique"},
	"extinguishers": {"طفايه", "طفايات", "extinguisher", "extincteur"},
	"smoke":         {"كاشف دخان", "كواشف الدخان", "smoke detector", "detecteur de fumee"},
	"lifting":       {"معدات الرفع", "lifting equipment", "sling", "shackle", "levage", "حزام رفع"},
	"welding":       {"لحام", "welding", "soudage"},
	"ppe":           {"معدات الوقايه", "ppe inventory", "epi "},
	"grinder":       {"جلخ", "grinder", "meuleuse", "grinding"},
	"firepump":      {"مضخه حريق", "مضخه الحريق", "fire pump", "motopompe"},
	"vehicle":       {"مركبه خفيفه", "سياره", "vehicle inspection", "vehicule leger", "light vehicle"},
}

func (a *app) assistChk(nq string, acts *[]map[string]interface{}) bool {
	n := 0
	for _, c := range chkTpls {
		for _, k := range chkKW[c.ID] {
			if strings.Contains(nq, k) {
				*acts = append(*acts, map[string]interface{}{"type": "chk", "id": c.ID, "label": "📋 افتح قائمة: " + c.Title.Ar})
				n++
				break
			}
		}
		if n >= 2 {
			break
		}
	}
	return n > 0
}

// assistGuide answers "how do I inspect X / what is Y" with the best inspection / well-control entry in full:
// explanation + numbered steps, plus buttons for the entry, related entries and the matching checklist.
var opKW = []struct {
	n  string
	kw []string
}{
	{"010", []string{"نقل الحفاره", "rig move", "تركيب الحفاره"}},
	{"030", []string{"مادلوغ", "mud logging"}},
	{"040", []string{"تنزيل الكيسنغ", "running casing", "تنزيل انابيب التغليف"}},
	{"050", []string{"اسمنت", "تسمنت", "سمنت", "cementing"}},
	{"060", []string{"كورينغ", "coring", "تكوير"}},
	{"070", []string{"قياسات كهربائيه", "wireline logging", "لوغينغ"}},
	{"080", []string{"تثقيب", "perforat"}},
	{"090", []string{"اختبار التكوين", "dst", "drill stem"}},
	{"100", []string{"الاختبار السطحي", "اختبار سطحي", "surface test"}},
	{"110", []string{"slickline", "سليك", "وايرلاين", "wireline"}},
	{"120", []string{"كويل", "coil tubing", "انبوب ملفوف"}},
	{"130", []string{"تحفيز", "نيتروجين", "تكسير", "acidiz", "حمض"}},
	{"140", []string{"اكمال", "completion"}},
	{"150", []string{"علوق", "صيد", "fishing", "stuck pipe"}},
	{"160", []string{"workover", "قتل البئر", "ووركوفر"}},
	{"020", []string{"الحفر والمناور", "tripping", "مناوره"}},
}

var reParen = regexp.MustCompile(`\([^)]*\)`)

func titleKey(t string) string {
	t = reParen.ReplaceAllString(t, " ")
	t = strings.NewReplacer("«", " ", "»", " ", ":", " ", "؟", " ", "?", " ", "،", " ", ",", " ", "/", " ", "\"", " ", "–", " ", "-", " ", ".", " ").Replace(t)
	t = normQ(t)
	for i := 0; i < 3; i++ {
		for _, w := range []string{"what is ", "what are ", "ما هو ", "ما هي ", "a ", "an ", "the ", "le ", "la ", "les ", "l ", "un ", "une ", "des ", "du ", "de la "} {
			if strings.HasPrefix(t, w) {
				t = strings.TrimPrefix(t, w)
			}
		}
	}
	return t
}

var exactNoise = []string{"what should i know about", "tell me about", "parle moi de", "parlez moi de", "dis moi", "how do i handle", "how to handle", "how do i stay safe around", "safety rules", "كيف اتعامل مع", "ماذا افعل بخصوص", "ما هي اخطار", "ما هي مخاطر", "اخطار", "مخاطر", "وكيف نتحكم بها", "كيف اراقب وافتش", "علي الحفاره", "على الحفاره", "ما هو", "ما هي", "اشرح لي", "اشرح", "what is", "what are", "c est quoi", "qu est ce que", "explain", "define", "et quels dangers"}

func stripNoise(nq string) string {
	q := " " + nq + " "
	for _, w := range exactNoise {
		q = strings.Replace(q, " "+w+" ", " ", -1)
	}
	return strings.TrimSpace(strings.Join(strings.Fields(q), " "))
}

// assistExact: the question is (a wrapper around) the exact title or tag of KB cards -> show those cards.
func (a *app) assistExact(raw, nq string, reply *string, acts *[]map[string]interface{}) bool {
	q1 := nq
	for _, w := range []string{"ما هو", "ما هي", "اشرح لي", "اشرح", "شرح", "what is", "what are", "explain", "c est quoi", "qu est ce que", "define"} {
		if strings.HasPrefix(q1, w+" ") {
			q1 = strings.TrimPrefix(q1, w+" ")
			break
		}
	}
	q := stripNoise(nq)
	k := titleKey(q)
	if len([]rune(k)) < 3 {
		return false
	}
	_, es := a.kbSearcher()
	var best *kbEntry
	var same []kbEntry
	for _, kk := range []string{titleKey(q1), k} {
		if len([]rune(kk)) < 3 {
			continue
		}
		for i := range es {
			e := es[i]
			for _, l := range []string{"ar", "en", "fr"} {
				if t := e.Title[l]; t != "" && titleKey(t) == kk {
					same = append(same, e)
					if best == nil || (best.Cat == "glossary" && e.Cat != "glossary") {
						x := e
						best = &x
					}
					break
				}
			}
		}
		if best != nil {
			break
		}
	}
	if best == nil {
		// tag match (strict), ranked by intent
		haz := hasAny(nq, "خطر", "اخطار", "مخاطر", "hazard", "danger", "safety", "اتعامل", "افعل", "handle")
		type cand struct {
			e  kbEntry
			sc int
		}
		var cs []cand
		for i := range es {
			e := es[i]
			if e.Cat == "signs" || ((e.Cat == "wellops") && anyOp(nq)) {
				continue
			}
			for _, t := range e.Tags {
				if titleKey(t) == k || titleKey(t) == titleKey(q1) {
					sc := 1
					if e.Cat == "inspect" || e.Cat == "wellops" {
						sc = 0
					}
					if (e.Cat == "glossary") != haz && sc > 0 {
						sc = 2
					}
					cs = append(cs, cand{e, sc})
					break
				}
			}
		}
		if len(cs) == 0 {
			return false
		}
		bi := 0
		for i, c := range cs {
			if c.sc > cs[bi].sc {
				bi = i
			}
		}
		x := cs[bi].e
		best = &x
		for i, c := range cs {
			if i != bi {
				same = append(same, c.e)
			}
		}
	}
	b := firstOf(best.Body["ar"], best.Body["fr"], best.Body["en"])
	var sb strings.Builder
	sb.WriteString("📖 " + firstOf(best.Title["ar"], best.Title["en"]) + "\n\n" + b)
	if st := best.Steps["ar"]; len(st) > 0 {
		sb.WriteString("\n\nالخطوات:")
		for i, s := range st {
			sb.WriteString(fmt.Sprintf("\n%d. %s", i+1, s))
		}
	}
	*reply = sb.String()
	*acts = append(*acts, map[string]interface{}{"type": "kb", "id": best.ID, "title": best.Title, "kind": best.Type, "label": "📖 فتح البطاقة كاملة"})
	n := 0
	for _, e := range same {
		if e.ID != best.ID && n < 4 {
			n++
			*acts = append(*acts, map[string]interface{}{"type": "kb", "id": e.ID, "title": e.Title, "kind": e.Type, "label": "📖 " + firstOf(e.Title["ar"], e.Title["en"])})
		}
	}
	return true
}

func (a *app) tagPin(q, nq string) bool {
	_, es := a.kbSearcher()
	sq := nq
	for _, n := range subjNoise {
		sq = strings.Replace(sq, n, " ", -1)
	}
	for _, n := range []string{"ما هي", "ما هو", "ما خطر", "كيف اراقب", "what is", "c est quoi", "كيف", "ما"} {
		sq = strings.Replace(sq, n+" ", " ", -1)
	}
	sq = strings.ToLower(strings.TrimSpace(strings.Join(strings.Fields(sq), " ")))
	if sq == "" {
		return false
	}
	for _, e := range es {
		if e.Cat == "inspect" || e.Cat == "wellops" {
			continue
		}
		for _, t := range e.Tags {
			if normQ(t) == normQ(sq) {
				return true
			}
		}
	}
	return false
}

func (a *app) assistGuide(q, nq string, reply *string, acts *[]map[string]interface{}) bool {
	si, es := a.kbSearcher()
	var best *kbEntry
	var rel []kbEntry
	var firstWO *kbEntry
	sq := nq
	for _, n := range subjNoise {
		sq = strings.Replace(sq, n, " ", -1)
	}
	for _, n := range []string{"ما هي", "ما هو", "ما خطر", "كيف اراقب", "كيف اقرا", "what is", "c est quoi", "كيف"} {
		sq = strings.Replace(sq, n, " ", -1)
	}
	sq = strings.Join(strings.Fields(sq), " ")
	if len(sq) < 3 || sq == nq {
		sq = q
	}
	for _, h := range si.search(sq, 40) {
		e := es[si.data.Chunks[h.Chunk].File]
		if e.Cat == "wellops" && firstWO == nil && len(e.ID) == 7 && e.ID[3] >= '0' && e.ID[3] <= '9' {
			x := e
			firstWO = &x
		}
		if e.Cat != "inspect" && e.Cat != "wellcontrol" && e.Cat != "drilling" && e.Cat != "wellops" && e.Cat != "emergency" {
			continue
		}
		if best == nil {
			x := e
			best = &x
		} else if len(rel) < 3 {
			rel = append(rel, e)
		}
	}
	pinned := false
		if !pinned && !anyOp(sq) {
		var words []string
		for _, w := range strings.Fields(strings.ToLower(sq)) {
			w = strings.TrimSuffix(w, "s")
			if len([]rune(w)) >= 3 && !hasAny(" the of and for are how what est que les des une ", " "+w+" ") {
				words = append(words, w)
			}
		}
		if len(words) > 0 {
			bestScore := 0
			var bestE *kbEntry
			for _, e := range es {
				if e.Cat == "inspect" || e.Cat == "wellops" {
					continue
				}
				txt := normQ(firstOf(e.Title["ar"], "") + " " + firstOf(e.Title["en"], "") + " " + firstOf(e.Title["fr"], "") + " " + strings.Join(e.Tags, " "))
				all := true
				for _, w := range words {
					if !strings.Contains(txt, normQ(w)) {
						all = false
						break
					}
				}
				if !all {
					continue
				}
				sc := 1
				for _, t := range e.Tags {
					if normQ(t) == normQ(sq) {
						sc = 3
					}
				}
				if e.Cat != "glossary" {
					sc++
				}
				if sc > bestScore {
					x := e
					bestScore, bestE = sc, &x
				}
			}
			if bestE != nil {
				if best != nil && best.ID != bestE.ID {
					rel = append([]kbEntry{*best}, rel...)
					if len(rel) > 3 {
						rel = rel[:3]
					}
				}
				best = bestE
				pinned = true
			}
		}
	}
	if !hasAny(nq, "simops", "تصريح", "ptw", "jsa", "tbt", "تصحيحي", "corrective", "قائمة", "checklist") {
		lsq := strings.ToLower(strings.TrimSpace(sq))
		for _, e := range es {
			if e.Cat == "inspect" || e.Cat == "wellops" {
				continue
			}
			for _, t := range e.Tags {
				if normQ(t) == normQ(lsq) {
					x := e
					if best != nil && best.ID != x.ID {
						rel = append([]kbEntry{*best}, rel...)
						if len(rel) > 3 {
							rel = rel[:3]
						}
					}
					best = &x
					pinned = true
					break
				}
			}
			if pinned {
				break
			}
		}
	}
	if best == nil {
		return false
	}
	// operations: prefer the operation cards (guide / hazards / meeting) unless a checklist was asked
	want := "G"
	if hasAny(nq, "simops", "تزامن", "متزامن") {
		var sims []kbEntry
		for _, e := range es {
			if strings.HasPrefix(e.ID, "WO-SIM") {
				sims = append(sims, e)
			}
		}
		if len(sims) > 0 {
			best = &sims[0]
			rel = sims[1:]
			want = "S"
		}
	}
	if want == "S" {
	} else if hasAny(nq, "تصحيحي", "corrective", "capa", "اجراء تصحيح") {
		want = "C"
	} else if hasAny(nq, "tbt", "toolbox", "حديث ما قبل", "حديث قبل", "حديث السلامه") {
		want = "T"
	} else if hasAny(nq, "jsa", "تحليل مخاطر", "تحليل المخاطر") {
		want = "J"
	} else if hasAny(nq, "تصريح", "ptw", "permit", "permis") {
		want = "P"
	} else if hasAny(nq, "اجتماع", "meeting", "reunion", "toolbox", "نقاش", "نتحدث", "نراقب") {
		want = "M"
	} else if hasAny(nq, "خطر", "اخطار", "hazard", "danger", "مخاطر") {
		want = "H"
	}
	if !pinned && want != "S" && !hasAny(nq, "قائمة", "فحص", "checklist", "inspect") {
		var wo *kbEntry
		if anyOp(nq) {
			wo = firstWO
		} else if best.Cat == "wellops" && len(best.ID) == 7 && best.ID[3] >= '0' && best.ID[3] <= '9' {
			x := *best
			wo = &x
		}
		for _, o := range opKW {
			if hasAny(nq, o.kw...) {
				for _, e := range es {
					if e.ID == "WO-"+o.n+"G" {
						x := e
						wo = &x
					}
				}
				break
			}
		}
		if want == "P" && wo == nil || want == "P" && !anyOp(nq) {
			id := "WO-PTW1"
			if hasAny(nq, "ساخن", "بارد", "اي تصريح", "نوع", "hot", "cold", "type") {
				id = "WO-PTW0"
			}
			for _, e := range es {
				if e.ID == id {
					x := e
					wo = &x
				}
			}
			if wo != nil {
				best = wo
				wo = nil
			}
		}
		if wo != nil {
			for _, e := range es {
				if e.ID == wo.ID[:6]+want {
					x := e
					wo = &x
					break
				}
			}
			best = wo
		}
		if want == "C" && best != nil {
			id := "WO-CA0"
			if anyOp(nq) && wo != nil && len(wo.ID) == 7 {
				id = "WO-" + wo.ID[3:6] + "C"
			}
			for _, e := range es {
				if e.ID == id {
					x := e
					best = &x
				}
			}
		}
		if want == "T" && best != nil && len(best.ID) == 7 {
			for _, e := range es {
				if e.ID == "TB-W"+best.ID[3:6] {
					x := e
					best = &x
				}
			}
		}
		if want == "J" && best != nil && len(best.ID) == 7 {
			if ids := opJSA[best.ID[3:6]]; len(ids) > 0 {
				for _, e := range es {
					if e.ID == ids[0] {
						x := e
						best = &x
					}
				}
			}
		}
	}
	if len(best.Steps["ar"]) == 0 && !pinned {
		for i, e := range rel {
			if len(e.Steps["ar"]) > 0 {
				x := *best
				rel[i] = x
				y := e
				best = &y
				break
			}
		}
	}
	b := firstOf(best.Body["ar"], best.Body["fr"], best.Body["en"])
	var sb strings.Builder
	sb.WriteString("📖 " + firstOf(best.Title["ar"], best.Title["en"]) + "\n\n" + b)
	if st := best.Steps["ar"]; len(st) > 0 {
		sb.WriteString("\n\nالخطوات:")
		for i, s := range st {
			sb.WriteString(fmt.Sprintf("\n%d. %s", i+1, s))
		}
	}
	if len(rel) > 0 {
		sb.WriteString("\n\nمواضيع ذات صلة في الأسفل ⬇")
	}
	*reply = sb.String()
	a.assistChk(nq, acts)
	*acts = append(*acts, map[string]interface{}{"type": "kb", "id": best.ID, "title": best.Title, "kind": best.Type, "label": "📖 فتح البطاقة كاملة"})
	if strings.HasPrefix(best.ID, "WO-") && len(best.ID) == 7 {
		rel = rel[:0]
		for _, e := range es {
			if e.ID != best.ID && strings.HasPrefix(e.ID, best.ID[:6]) {
				rel = append(rel, e)
			}
		}
	}
	if want == "S" {
	} else if strings.HasPrefix(best.ID, "TB-W") {
		rel = rel[:0]
		for _, e := range es {
			if e.ID == "WO-"+best.ID[4:]+"G" || e.ID == "WO-"+best.ID[4:]+"M" || e.ID == "WO-"+best.ID[4:]+"P" {
				rel = append(rel, e)
			}
		}
	} else if strings.HasPrefix(best.ID, "JS-") {
		rel = rel[:0]
		for n, ids := range opJSA {
			for _, id := range ids {
				if id == best.ID {
					for _, e := range es {
						if e.ID == "WO-"+n+"P" || e.ID == "WO-"+n+"G" || e.ID == "WO-"+n+"H" {
							rel = append(rel, e)
						}
					}
				}
			}
		}
	} else if strings.HasPrefix(best.ID, "WO-") && len(best.ID) == 7 {
		if ids := opJSA[best.ID[3:6]]; len(ids) > 0 {
			for _, e := range es {
				if e.ID == ids[0] || e.ID == "TB-W"+best.ID[3:6] {
					rel = append(rel, e)
				}
			}
		}
	}
	for _, e := range rel {
		*acts = append(*acts, map[string]interface{}{"type": "kb", "id": e.ID, "title": e.Title, "kind": e.Type, "label": "📖 " + firstOf(e.Title["ar"], e.Title["en"])})
	}
	return true
}

func (a *app) assistHandlers(guard func(http.HandlerFunc) http.HandlerFunc, mux *http.ServeMux) {
	mux.HandleFunc("/api/hse/assist", guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Text string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		writeJSON(w, a.assistRun(in.Text))
	}))
}

var opJSA = map[string][]string{"010": {"JS-018", "JS-019"}, "020": {"JS-001", "JS-007"}, "030": {"JS-111"}, "040": {"JS-016"}, "050": {"JS-017"}, "060": {"JS-111"}, "070": {"JS-021", "JS-102"}, "080": {"JS-101"}, "090": {"JS-103"}, "100": {"JS-104"}, "110": {"JS-105"}, "120": {"JS-106"}, "130": {"JS-107"}, "140": {"JS-108"}, "150": {"JS-109"}, "160": {"JS-110"}}

func anyOp(nq string) bool {
	for _, o := range opKW {
		if hasAny(nq, o.kw...) {
			return true
		}
	}
	return false
}
