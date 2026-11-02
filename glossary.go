package main

import (
	"strings"
	"sync"
)

// Cross-language HSE glossary: each line is one concept, variants separated by "|"
// (English, Arabic, French; several words per language separated by ",").
// Used to expand query terms so an Arabic question can find English/French passages and vice versa.
const glossaryRaw = `
stopcard|بطاقة ستوب,بطاقات ستوب,ستوب|carte stop,stop card|STOP卡,观察卡
unsafe act|سلوك غير آمن,تصرف غير آمن,عمل غير آمن|acte dangereux|不安全行为
unsafe condition|حالة غير آمنة,ظرف غير آمن,وضع غير آمن|condition dangereuse|不安全状态,不安全条件
positive observation|ملاحظة إيجابية,ممارسة جيدة|observation positive,bonne pratique|正面观察,良好实践
reaction|رد فعل,ردة فعل|réaction|人员反应
housekeeping|ترتيب,نظافة المكان,تنظيم|ordre,rangement|整理整顿,现场整洁
derrickman|ديريكمان,عامل البرج|accrocheur|井架工
floorman|عامل أرضية الحفر,عامل الأرضية|plancher|钻工,场地工
roustabout|عامل مساعد,روستاباوت|manœuvre|杂工
driller|حفار,سوندور|sondeur|司钻
toolpusher|رئيس الحفر,مشرف الحفر|superviseur forage|平台经理
mud|طين الحفر,الطين|boue|泥浆
tanks|أحواض,خزانات|bacs|泥浆罐,罐
cellar|حفرة,قبو|cave|方井
substructure|قاعدة,هيكل سفلي|substructure|底座
choke|خانق|duse|节流
manifold|مشعب|manifold|管汇
casing|أنابيب التغليف,تغليف|tubage|套管
cement|إسمنت,اسمنت|ciment|水泥
tripping|رحلة|manoeuvre|起下钻
rig|جهاز الحفر,حفارة|sonde,appareil|钻机
kick|ركلة,تدفق|kick,venue|井涌
pickup|سيارة,بيك أب|pickup|皮卡
sling,slings|حبال,حبل رفع|élingue,élingues|吊索
hose|خرطوم|flexible,tuyau|软管
slip,slips,trip,trips|انزلاق,تعثر|glissade,trébuchement|滑倒,绊倒
phone|هاتف,جوال|téléphone|手机
walkway|ممر|passerelle|通道
stairs|درج,سلم|escalier|楼梯
fire|حريق,نار,حرائق|incendie,feu|火灾,火,着火,失火
extinguisher,extinguishers|طفاية,طفايات,مطفأة,مطافئ|extincteur,extincteurs|灭火器
alarm|إنذار,انذار|alarme,alerte|报警,警报
evacuation,evacuate|إخلاء,اخلاء|évacuation,evacuation,evacuer|疏散,撤离
emergency|طوارئ,طارئ,طارئة|urgence,secours|紧急,应急,突发
exit|مخرج,مخارج|sortie,issue|出口,通道
smoke|دخان|fumée,fumee|烟,烟雾
flammable|قابل للاشتعال,مشتعل,اشتعال|inflammable|易燃,可燃
explosion,explosive|انفجار,متفجر,متفجرات|explosion,explosif|爆炸,易爆
ignition|إشعال|inflammation,allumage|点火,着火源
safety|سلامة,أمان,امان|sécurité,securite|安全
health|صحة|santé,sante|健康,卫生
environment|بيئة|environnement|环境,环保
hazard,hazards|خطر,أخطار,مخاطر,خطورة|danger,dangers|危险,危害,隐患,危险源
risk,risks|مخاطر,خطر|risque,risques|风险
accident,accidents|حادث,حوادث|accident,accidents|事故
incident|حادثة,واقعة|incident|事件
injury,injuries|إصابة,إصابات|blessure,blessures,lésion|受伤,伤害,工伤
fatality|وفاة,وفيات|décès,mortel|死亡,伤亡
first aid|إسعاف,إسعافات,اسعاف|secourisme,secours|急救,救护
burn,burns|حرق,حروق|brûlure,brulure,brûlures|烧伤,灼伤
bleeding|نزيف|saignement,hémorragie|出血
fracture|كسر|fracture|骨折
unconscious|إغماء,فاقد|inconscient,évanouissement|昏迷,晕倒
resuscitation,cpr|إنعاش|réanimation,reanimation|心肺复苏
ppe|معدات,وقاية|epi,équipements|个人防护,防护装备,劳保
protective,protection|وقاية,حماية,واقية|protection,protecteur,protectifs|防护,保护
helmet|خوذة|casque|安全帽,头盔
gloves,glove|قفازات,قفاز|gants,gant|手套
goggles,glasses|نظارات,نظارة|lunettes|护目镜,眼镜
mask,respirator|كمامة,قناع|masque,respirateur|口罩,面罩,呼吸器
boots,shoes|حذاء,أحذية|chaussures,bottes|安全鞋,鞋,靴
harness,lanyard|حزام,أحزمة|harnais,longe|安全带,安全绳
vest|سترة|gilet|反光背心,背心
earplugs,hearing|سدادات,سمع|bouchons,auditive|耳塞,听力
noise|ضوضاء,ضجيج|bruit|噪声,噪音
vibration|اهتزاز|vibration|振动
dust|غبار|poussière,poussiere|粉尘,灰尘
gas|غاز|gaz|气体,燃气
chemical,chemicals|كيميائية,كيميائي,كيماويات|chimique,chimiques,produits|化学,化学品
toxic|سام,سامة,سموم|toxique,toxiques|有毒,毒性
corrosive|أكال,أكالة,تآكل|corrosif|腐蚀,腐蚀性
msds,sds|بطاقة بيانات السلامة|fds,fiche|安全数据表
ghs|النظام المتوافق عالميا|sgh|全球统一分类
pictogram|رمز,رموز|pictogramme|象形图,图标
label,labelling|ملصق,وسم|étiquette,etiquette,étiquetage|标签,标识
storage,store|تخزين|stockage,stocker|储存,存放,仓库
waste|نفايات,مخلفات|déchets,dechets|废物,废弃物
hazardous waste|نفايات خطرة|déchets dangereux|危险废物
spill|تسرب,انسكاب|déversement,fuite|泄漏,溢出
leak|تسرب,تسريب|fuite|泄露,渗漏
pollution|تلوث|pollution|污染
emission,emissions|انبعاثات,انبعاث|émission,émissions|排放
carbon|كربون|carbone|碳
ventilation|تهوية|ventilation,aération|通风
lighting|إضاءة|éclairage|照明
temperature|حرارة,درجة|température|温度
heat|حر,حرارة|chaleur|高温,热
cold|برد|froid|低温,寒冷
radiation|إشعاع|rayonnement,radiation|辐射
electric,electrical,electricity|كهرباء,كهربائي,كهربائية|électrique,électricité,electricite|电,电气,电力,触电
shock,electrocution|صعق,صدمة|électrocution,choc|电击,触电
lockout,tagout,loto|قفل,عزل|consignation,cadenassage,déconsignation|上锁挂牌,挂牌上锁
isolation,isolate|عزل|isolement,isoler|隔离
machine,machinery,equipment|آلة,آلات,ماكينة,ماكينات,معدة|machine,machines,équipement|机器,设备,机械
guard,guarding|حاجز,حواجز,غطاء|protecteur,carter|防护罩,护栏
maintenance|صيانة|maintenance|维护,维修,检修
inspection,inspect|فحص,تفتيش|inspection,inspecter,contrôle|检查,巡检
audit|تدقيق,مراجعة|audit|审核,审计
training|تدريب|formation|培训
induction|توجيه|accueil|入场教育,入职培训
permit|تصريح,رخصة|permis|许可,许可证
ptw|تصريح عمل|permis|作业许可
work|عمل|travail|工作,作业
height,heights|ارتفاع,ارتفاعات,مرتفعة,مرتفع|hauteur,hauteurs|高处,高度
fall,falls,falling|سقوط|chute,chutes|坠落,跌倒,掉落
ladder,ladders|سلم,سلالم|échelle,echelle,échelles|梯子,梯
scaffold,scaffolding|سقالة,سقالات|échafaudage,échafaudages|脚手架
excavation,trench|حفر,حفريات,خندق|fouille,tranchée,excavation|开挖,挖掘,沟槽
crane,lifting|رافعة,رفع|grue,levage|起重机,吊装,起重
forklift|رافعة شوكية,شوكية|chariot|叉车
load,loads|حمولة,حمل|charge,charges|载荷,负载,荷载
vehicle|مركبة,مركبات|véhicule,véhicules|车辆
driving,driver|قيادة,سائق|conduite,conducteur|驾驶,司机
confined|مغلقة,محصورة,ضيقة|confiné,confinés|受限
space|حيز,فراغ,أماكن,الأماكن|espace|空间
entry,enter|دخول,الدخول|entrée,entrer|进入
welding|لحام|soudage,soudure|焊接
cutting|قطع|coupe,découpe|切割
hot|ساخنة,ساخن|chaud|动火
construction|بناء,إنشاءات|construction,chantier|施工,建筑
site|موقع,مواقع|site,chantier|现场,工地
worker,workers,employee,employees|عامل,عمال,موظف,موظفين|travailleur,travailleurs,employé,salarié|工人,员工,职工
employer|صاحب,رب|employeur|雇主,用人单位
manager,supervisor|مشرف,مسؤول,مدير|responsable,superviseur|主管,经理,负责人
responsibility,responsibilities|مسؤولية,مسؤوليات|responsabilité,responsabilités|责任,职责
law,regulation,regulations|قانون,قوانين,لائحة,لوائح,تنظيم|loi,réglementation,règlement|法律,法规,条例,规定
standard,standards|معيار,معايير,مواصفة|norme,normes|标准,规范
iso|أيزو|iso|iso
limit,limits|حد,حدود|limite,limites,valeur|限值,限制,极限
exposure|تعرض|exposition|接触,暴露
occupational|مهنية,مهني|professionnel,professionnelle|职业
disease,illness|مرض,أمراض|maladie,maladies|疾病,职业病
ergonomics,ergonomic|بيئة العمل|ergonomie,ergonomique|人体工程学,人机工程
posture|وضعية|posture|姿势
manual|يدوي,يدوية|manutention|手动,搬运
back|ظهر|dos|腰背
stress|إجهاد,ضغط نفسي|stress|压力,应激
fatigue|تعب,إرهاق|fatigue|疲劳
procedure,procedures|إجراء,إجراءات|procédure,procédures|程序,流程,规程
plan|خطة|plan|计划,方案
assessment|تقييم|évaluation|评估,评价
control,controls|ضبط,مكافحة,رقابة|maîtrise,contrôle|控制,管控
prevention,prevent|منع,وقاية|prévention,prévenir|预防
investigation|تحقيق|enquête|调查
report,reporting|تقرير,إبلاغ,تبليغ|rapport,déclaration|报告,汇报
nearmiss|كاد|presqu'accident|未遂事故,险兆事件
signs,signage,sign|إشارة,إشارات,لافتات,لافتة|panneau,panneaux,signalisation|标志,标识,警示牌
colour,color,colours|لون,ألوان|couleur,couleurs|颜色
water|ماء,مياه|eau|水
drinking|شرب|potable|饮用
hygiene|نظافة|hygiène,hygiene|卫生
washing|غسل|lavage|清洗,洗涤
hands|يدين,أيدي,اليدين|mains|手
food|غذاء,طعام|aliment,alimentaire|食品,食物
infection,infectious|عدوى|infection|感染
virus,bacteria|فيروس,بكتيريا|virus,bactérie|病毒,细菌
asbestos|أسبستوس,أسبست|amiante|石棉
lead|رصاص|plomb|铅
silica|سيليكا|silice|二氧化硅,矽
benzene|بنزين|benzène|苯
solvent,solvents|مذيب,مذيبات|solvant,solvants|溶剂
acid,acids|حمض,أحماض|acide,acides|酸
battery|بطارية|batterie|电池
pressure|ضغط|pression|压力
vessel,tank|وعاء,خزان|réservoir,cuve|容器,储罐,罐
cylinder,cylinders|أسطوانة,أسطوانات,اسطوانات|bouteille,bouteilles|气瓶,钢瓶
pipe,pipes,pipeline|أنبوب,أنابيب,خط|tuyau,tuyaux,canalisation|管道,管线
valve|صمام|vanne,soupape|阀门,阀
sprinkler|رشاش,رشاشات|sprinkler|喷淋
hydrant|صنبور حريق,حنفية|poteau|消火栓
assembly,muster|تجمع|rassemblement|集合,集合点
warning|تحذير|avertissement|警告
caution|تنبيه,احتياط|attention|注意
danger|خطر|danger|危险
prohibited,forbidden|ممنوع,محظور|interdit|禁止
mandatory|إلزامي,إجباري|obligatoire|必须,强制
rescue|إنقاذ|sauvetage|救援
ambulance|إسعاف,سيارة|ambulance|救护车
doctor,medical|طبيب,طبي|médecin,médical|医生,医疗
pregnancy|حمل|grossesse|怀孕
women|نساء|femmes|女工,女性
children,minors|أطفال,قاصرين|enfants,mineurs|儿童,未成年
night|ليل,ليلي|nuit|夜班,夜间
hours|ساعات|heures|小时,工时
rest|راحة|repos|休息
break|استراحة|pause|休息
alcohol|كحول|alcool|酒精
drugs|مخدرات|drogues|毒品
smoking|تدخين|tabac,fumer|吸烟
policy|سياسة|politique|政策,方针
compliance|امتثال,التزام|conformité|合规
guideline|دليل إرشادي,توجيهات|directive|指南
biological|بيولوجي,بيولوجية,حيوي|biologique|生物
psychosocial|نفسي,نفسية|psychosocial|心理
rootcause|جذرية,جذري|cause profonde|根本原因
corrective|تصحيحي,تصحيحية|correctif,corrective,corrective|纠正
preventive|وقائي,وقائية|préventif,préventive|预防性
unsafe|غير آمن,غير آمنة|dangereux,dangereuse|不安全
act|فعل,سلوك|acte|行为
condition|حالة,ظرف|condition|状态,条件
lti|إصابة مع توقف|arrêt|工伤损失工时,误工
toolbox|حديث,نقاش,اجتماع|causerie|班前会,工具箱会议
briefing|إحاطة,إيجاز|briefing|交底,简报
jsa|تحليل سلامة الوظيفة,تحليل|analyse|工作安全分析
method|طريقة,منهجية|mode opératoire|方法,施工方案
drill,exercise|تمرين,تدريب,مناورة|exercice|演练
kit|حقيبة,طقم|trousse|急救箱,套件
eyewash|غسل العين,عيون|rince-oeil,oculaire|洗眼器
shower|دش,رشاش|douche|淋浴,喷淋
restricted|مقيدة,محظورة|restreint,restreinte|限制
nonconformity|عدم مطابقة,عدم المطابقة|non-conformité|不符合项,不合格
observation|ملاحظة,ملاحظات|observation|观察,意见
finding|نتيجة,نتائج|constat|审核发现
review|مراجعة|revue|评审,评审会
improvement|تحسين|amélioration|改进,持续改进
kpi|مؤشر,مؤشرات|indicateur,indicateurs|关键绩效指标,指标
frequency|تكرار|fréquence|频率
severity|جسامة,خطورة,شدة|gravité|严重程度,严重率
rate|معدل|taux|率,比率
manhours|ساعات عمل|heures travaillées|工时,人工时
impact|أثر,تأثير|impact|影响
remediation|معالجة,إصلاح|dépollution,remédiation|修复,治理
containment|احتواء|confinement,rétention|围堵,围油
sustainability|استدامة|durabilité|可持续
renewable|متجددة|renouvelable|可再生
stakeholder|أصحاب المصلحة|parties prenantes|相关方
certification|اعتماد,شهادة|certification|认证
management|إدارة|management,système|管理
pdca|دورة|cycle|循环
inhalation|استنشاق|inhalation|吸入
ingestion|ابتلاع|ingestion|食入
skin|جلد|cutané,peau|皮肤
oil|نفط,زيت|pétrole,huile,pétrolier|石油,油
gasfield|حقل|champ|油气田
offshore|بحرية,بحري|offshore|海上
platform|منصة|plateforme|平台
rig|حفار,جهاز حفر|appareil,forage|钻机,井架
drilling|حفر,الحفر|forage|钻井
well,wellhead|بئر,آبار|puits,tête|井,井口
refinery|مصفاة|raffinerie|炼油厂
plant|مصنع,محطة|usine|工厂,装置
flare|شعلة|torche|火炬
h2s|كبريتيد الهيدروجين|sulfure|硫化氢
bop|مانع|obturateur|防喷器
esd|إيقاف طارئ|arrêt d'urgence|紧急关断
hazop|دراسة|étude|危险与可操作性分析
sil|مستوى سلامة|niveau d'intégrité|安全完整性等级
scba|جهاز تنفس|appareil respiratoire|正压式空气呼吸器
detector|كاشف,كواشف|détecteur,détecteurs|探测器,检测仪
lifeboat|قارب نجاة|canot|救生艇
pob|عدد الأفراد,أفراد|personnel|在岗人数,现场人员
board|متواجدين,على متن|bord|在岗
bypass,bypassing,override,inhibit|تجاوز,تخطي,تعطيل,إلغاء|shunt,shuntage,neutralisation,inhibition,contournement|旁路,短接,屏蔽,跨接
pointage,attendance,timesheet,clocking|حضور,الحضور,انصراف,الانصراف,تسجيل,دوام|pointage,présence,presence,pointer|考勤,打卡,出勤
absence,absent|غياب,غائب|absence,absent|缺勤,缺席
arrival,departure|وصول,دخول,خروج,مغادرة|arrivée,départ|到达,离开,上班,下班
overtime|إضافي,إضافية|supplémentaires,supplémentaire|加班
leave,vacation|إجازة,عطلة|congé,congés|休假,请假
safeguard,interlock|منظومة أمان,تأمين|sécurité|安全联锁,联锁
simops|عمليات متزامنة|simultanées|同时作业
moc|إدارة التغيير,تغيير|changement|变更管理
sonatrach|سوناطراك|sonatrach|索纳特拉克
daily|يومي,يومية|journalier,quotidien|每日,日报
weekly|أسبوعي,أسبوعية|hebdomadaire|每周,周报
monthly|شهري,شهرية|mensuel,mensuelle|每月,月报
shift|وردية,مناوبة|équipe,poste,quart|班次,倒班
date|تاريخ|date|日期
name|اسم|nom|姓名,名称
signature|توقيع|signature|签名,签字
approval|اعتماد,موافقة|approbation,validation|批准,审批
quantity|كمية|quantité|数量
number|رقم,عدد|numéro,nombre|编号,数量
department|قسم,دائرة|service,département|部门
company|شركة|société,entreprise|公司
contractor|مقاول|entrepreneur,sous-traitant|承包商
subcontractor|مقاول من الباطن|sous-traitant|分包商
visitor|زائر,زوار|visiteur|访客
`

var (
	glossaryAlt  map[string][]string
	glossaryOnce sync.Once
)

func glossary() map[string][]string {
	glossaryOnce.Do(buildGlossary)
	return glossaryAlt
}

func buildGlossary() {
	glossaryAlt = map[string][]string{}
	for _, line := range strings.Split(glossaryRaw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var all []string
		for _, lang := range strings.Split(line, "|") {
			for _, v := range strings.Split(lang, ",") {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				for _, t := range tokenize(v) {
					all = append(all, t)
				}
				if isArabicRune([]rune(v)[0]) {
					// a stem that starts with a prefix-like "ال" (e.g. الأماكن -> ماكن)
					for _, t := range tokenize("ال" + v) {
						all = append(all, t)
					}
				}
			}
		}
		for _, a := range all {
			for _, b := range all {
				if a != b && !hasStr(glossaryAlt[a], b) {
					glossaryAlt[a] = append(glossaryAlt[a], b)
				}
			}
		}
	}
}

func hasStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
