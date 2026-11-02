
== Vehicles & drivers (People > 🚗) ==
- Per-vehicle and per-driver documents with expiry dates (carte grise, assurance, CT, site access, licence, medical...). Custom documents allowed.
- Owner company (suggested from POB companies) with phone/email; saved in a company directory and auto-filled next time.
- Colour-coded expiry (red expired / orange within alert window / green OK); alert window setting (default 30 days).
- Alerts also appear in the Today reminders.
- STOP cards: each card's origin (manual: log/gwdc/register/mine, generated: dict/ai/generic) is kept in Source for later statistics.

== STOP cards database, company models, knowledge base 814 ==
- Ready database: ~11,200 cleaned STOP cards (Sonatrach logs, SP236 register, GW112 register) load once on first start (no observer names; duplicates skipped). Button: STOP > database > "Import the ready card database".
- Two small company models (Sonatrach / Sinopec-Chinese contractors) classify Nature / Class / Risk. Typing a new card shows the model's suggestion with a confidence value; you confirm or correct, and it learns. "Measure model accuracy" reports hold-out accuracy.
- Origin stats: Source separates manual (log/gwdc/register/mine) from generated (dict/ai/generic).
- Typo fixing uses a curated dictionary mined from the cards (no automatic fuzzy matching).
- Knowledge base: +22 entries filling gaps found against the cards (forklift, MEWP, stairs/handrails, noise, gloves, cylinders, lighting, manual handling, contractors, land transport, phones, smoking, lifting accessories, food safety, Legionella, generators, containers).

== Inspection Center (Field > 📋 Checklists), knowledge base 869, Sonatrach kick drills ==
- 18 digitised HSE-MS checklists (kitchen, camp, forklift, crane, rig safety, well control, SCBA, grounding, drop prevention, clinic, extinguishers, smoke detectors, lifting, welding, PPE, grinder, fire pump, vehicle). Overview tiles (due / done this week / never done), progress bars, optional reminders in Today.
- Fill form: section navigator, ✓/✗/— buttons, numeric ranges highlighted, 💡 how-to hint per item, ❓ asks the assistant about the item, ℹ opens the plain-language KB explanation, sticky progress bar. Each save feeds the inspection log and the daily report. Printable.
- Knowledge base: +55 beginner-level entries (FIFO/FEFO/LIFO, food temperatures, crane/wire rope/hooks, well control per Sonatrach, accumulator, BOP tests, SCBA, grounding, fire pump...). Several flagged "verify".
- Assistant: "how do I inspect X / what are the steps…" now answers with the full explanation and numbered steps, plus buttons to open the checklist and related cards.
- Drills: Kick (tripping) and Kick (drilling) templates from the Sonatrach procedure; field "shut-in time (s)" is evaluated against the 2-minute target and written into positives/improvements.
- Note: the original kitchen form lists the cold room at -10 °C; the checklist uses <= 5 °C for chilled and flags the form error.

== Well operations knowledge (new) ==
- Category "Well operations" (wellops): for each operation (rig move, drilling, mud logging, casing, cementing, coring, wireline logging, perforating, DST, surface testing, slickline, coil tubing, stimulation/nitrogen, completion, fishing, workover/P&A, pressure vessels) three cards: Guide (what it is + steps), Hazards (hazard - control), Safety meeting (talking points + what to watch).
- Extra cards from the training decks: defensive driving, driving standard, JSA, HSE passport, STOP cycle, risk matrix, hand safety, wire rope clips, sour-service rule, ESD/barriers.
- 116 operation STOP cards added to the STOP database on first start (source generic, unverified).
- The chat assistant answers "how does cementing work / safety meeting before perforating / hazards of coil tubing".
- Knowledge base: 933 entries. All content is assistant material: verify against company procedures.
- Permit to work (PTW) for every well operation: master table (hot / cold / confined space / electrical / lifting / radiation / explosives), a step-by-step "how to fill the permit" card, and one permit card per operation (WO-nnnP). Knowledge base: 951 entries.
- JSA for every operation permit (JS-101 to JS-113 plus existing JS-016/017/018/021): step - hazard - control tables, linked from the permit cards. Knowledge base: 964 entries.
- Toolbox talk (TBT) for every well operation (TB-W010 to TB-W160): real-style example, 5 key points and 3 questions to ask the crew. Knowledge base: 980 entries.
- Gap fill: 46 terms (equipment, drills, MOC, bow-tie, red zone, radio silence...), corrective-action guide + per-operation corrective actions, SIMOPS (guide, matrix, examples), 12 new rig signs (red zone, radio silence, explosives, nitrogen, acid, hot work, wireline, flare, confined space, SIMOPS, well test, line test). Knowledge base: 1058 entries.

=== اختبار الأسئلة العشوائية (1000 سؤال HSE حفر نفط وغاز) ===
- اختُبر المساعد بـ 940 سؤالاً (235 موضوعاً × 4 صيغ: عربي/فرنسي/إنجليزي).
- النتيجة: 58% -> 91.5% بعد إضافة 105 مصطلحات (GL-P01..71, GL-Q01..34) وتحسين التوجيه.
- التحسين: إزالة عبارات التغليف من السؤال قبل البحث، وتثبيت البطاقة عند تطابق الوسم تماماً مع الموضوع.
- قاعدة المعرفة: 1163 مادة. المخرجات تبقى للمراجعة البشرية.

=== دفعة ملفات 22 أكتوبر (19 عرض ppt + PDF) ===
- أضيف 27 مادة: أوراق بيانات سلامة (كبريتيك، هيدروكلوريك، كلوروفورم، كربيد الكالسيوم، بروميد الزنك)، قراءة SDS والخطوات التسع لتقييم الأخطار،
  أدوات APR واختبار الآبار، الكهرباء، الحريق وأنواع الطفايات، الإسعاف الأولي، أسطوانات الغاز، إصابات اليد والقفازات، ضغط الدم، المزاح الخطر،
  إدارة الرحلات وPOWERS وإرهاق السائق. قاعدة المعرفة: 1190 مادة.
- ملاحظة: شريحة الإنعاش في الملف تذكر نسب قديمة (5:1، 15:2)؛ البطاقة تذكر الإرشاد الحديث 30:2 وتنبّه لذلك.
- اختبار: 882/940 (93.9%) على الأسئلة العشوائية، و48/53 على أسئلة الدفعة الجديدة.

=== إصلاح الأسئلة التي لا يجيب عنها المساعد (23 أكتوبر) ===
- السبب الجذري: بطاقات العمليات (wellops) كانت تطغى على نتائج البحث وتحجب بطاقات المصطلحات والأخطار، والبحث كان يقتصر على فئات محددة.
- الحل: تثبيت البطاقة التي يطابق عنوانها/وسومها موضوع السؤال في كل الفئات، وإزالة عبارات الصياغة (ما هي/ما هو/c'est quoi/how do I stay safe).
- النتيجة: 893-894 من 940 (≈95%) بعد أن كانت 58% ثم 86%. الباقي في الغالب إجابة صحيحة بصياغة مختلفة عن الكلمة المفتاحية.

=== الوصول إلى 100% في اختبارات الإجابة (23 أكتوبر) ===
- مطابقة العنوان: إذا كان السؤال هو عنوان بطاقة (بأي لغة، مع أو بدون «ما هو/اشرح/what is») تظهر البطاقة مباشرة مع بطاقات المتطابقة العنوان.
- مطابقة الوسم: أي مصطلح يطابق وسماً (tag) يُجاب مباشرة، وتُرتَّب النتائج حسب النية (تعريف أو أخطار).
- إزالة صيغ الأسئلة (parle moi de / what should I know about / كيف أتعامل مع / ماذا أفعل بخصوص...).
- نتائج: عناوين كل البطاقات بالصيغ الثلاث 3264/3264، أسئلة الموضوعات 940/940، عيّنات عشوائية 300/300 و400/400 (حوالي 99.5% على بذور أخرى).
- 100% تعني: كل بطاقة قابلة للوصول بعنوانها وبوسومها. الأسئلة الحرة الجديدة جداً قد تحتاج إضافة وسوم؛ يُنصح بتسجيل الأسئلة غير المجابة.
