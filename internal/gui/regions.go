package gui

// regionNames are the countries and regions a node's name can point at, in
// English and in Chinese. A code that is not listed is shown as it is.
var regionNames = map[string][2]string{
	"HK": {"Hong Kong", "香港"},
	"MO": {"Macao", "澳门"},
	"TW": {"Taiwan", "台湾"},
	"CN": {"China", "中国大陆"},
	"JP": {"Japan", "日本"},
	"KR": {"South Korea", "韩国"},
	"SG": {"Singapore", "新加坡"},
	"US": {"United States", "美国"},
	"CA": {"Canada", "加拿大"},
	"MX": {"Mexico", "墨西哥"},
	"BR": {"Brazil", "巴西"},
	"AR": {"Argentina", "阿根廷"},
	"CL": {"Chile", "智利"},
	"CO": {"Colombia", "哥伦比亚"},
	"PE": {"Peru", "秘鲁"},
	"GB": {"United Kingdom", "英国"},
	"IE": {"Ireland", "爱尔兰"},
	"DE": {"Germany", "德国"},
	"FR": {"France", "法国"},
	"NL": {"Netherlands", "荷兰"},
	"BE": {"Belgium", "比利时"},
	"LU": {"Luxembourg", "卢森堡"},
	"CH": {"Switzerland", "瑞士"},
	"AT": {"Austria", "奥地利"},
	"IT": {"Italy", "意大利"},
	"ES": {"Spain", "西班牙"},
	"PT": {"Portugal", "葡萄牙"},
	"SE": {"Sweden", "瑞典"},
	"NO": {"Norway", "挪威"},
	"FI": {"Finland", "芬兰"},
	"DK": {"Denmark", "丹麦"},
	"IS": {"Iceland", "冰岛"},
	"PL": {"Poland", "波兰"},
	"CZ": {"Czechia", "捷克"},
	"RO": {"Romania", "罗马尼亚"},
	"HU": {"Hungary", "匈牙利"},
	"BG": {"Bulgaria", "保加利亚"},
	"GR": {"Greece", "希腊"},
	"UA": {"Ukraine", "乌克兰"},
	"RU": {"Russia", "俄罗斯"},
	"TR": {"Türkiye", "土耳其"},
	"IL": {"Israel", "以色列"},
	"AE": {"United Arab Emirates", "阿联酋"},
	"SA": {"Saudi Arabia", "沙特阿拉伯"},
	"IN": {"India", "印度"},
	"PK": {"Pakistan", "巴基斯坦"},
	"BD": {"Bangladesh", "孟加拉国"},
	"LK": {"Sri Lanka", "斯里兰卡"},
	"TH": {"Thailand", "泰国"},
	"VN": {"Vietnam", "越南"},
	"MY": {"Malaysia", "马来西亚"},
	"ID": {"Indonesia", "印度尼西亚"},
	"PH": {"Philippines", "菲律宾"},
	"KH": {"Cambodia", "柬埔寨"},
	"KZ": {"Kazakhstan", "哈萨克斯坦"},
	"AU": {"Australia", "澳大利亚"},
	"NZ": {"New Zealand", "新西兰"},
	"ZA": {"South Africa", "南非"},
	"NG": {"Nigeria", "尼日利亚"},
	"EG": {"Egypt", "埃及"},
}

// regionName is a region as the person reads it ("Japan", "日本"); the code
// itself when nothing better is known.
func (a *App) regionName(code string) string {
	if n, ok := regionNames[code]; ok {
		if a.lang == "zh" {
			return n[1]
		}
		return n[0]
	}
	return code
}
