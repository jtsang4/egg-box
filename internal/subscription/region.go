package subscription

import (
	"strings"
	"unicode"
)

// Region guesses the country or region of a node from its name, as an ISO 3166
// code ("HK", "JP"...). It returns "" when nothing matches. Providers put the
// location in the name, as a flag emoji or in words, so that is all there is
// to go by.
func Region(name string) string {
	if code := flagRegion(name); code != "" {
		return code
	}
	lower := strings.ToLower(name)
	tokens := strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, r := range regionTable {
		for _, w := range r.words {
			if len(w) <= 3 && isASCII(w) {
				for _, t := range tokens {
					if t == w || trimDigits(t) == w {
						return r.code
					}
				}
				continue
			}
			if strings.Contains(lower, w) {
				return r.code
			}
		}
	}
	return ""
}

func trimDigits(s string) string { return strings.TrimRight(s, "0123456789") }

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// flagRegion reads a regional-indicator pair (a flag emoji) anywhere in s.
func flagRegion(s string) string {
	var prev rune
	for _, r := range s {
		if r >= 0x1F1E6 && r <= 0x1F1FF {
			if prev != 0 {
				return string([]rune{'A' + prev - 0x1F1E6, 'A' + r - 0x1F1E6})
			}
			prev = r
			continue
		}
		prev = 0
	}
	return ""
}

var regionTable = []struct {
	code  string
	words []string
}{
	{"HK", []string{"香港", "hong kong", "hongkong", "hk"}},
	{"MO", []string{"澳门", "澳門", "macao", "macau"}},
	{"TW", []string{"台湾", "台灣", "台北", "taiwan", "taipei", "tw"}},
	{"JP", []string{"日本", "东京", "東京", "大阪", "japan", "tokyo", "osaka", "jp"}},
	{"SG", []string{"新加坡", "狮城", "獅城", "singapore", "sg"}},
	{"KR", []string{"韩国", "韓國", "首尔", "首爾", "korea", "seoul", "kr"}},
	{"US", []string{"美国", "美國", "洛杉矶", "圣何塞", "西雅图", "纽约", "硅谷", "芝加哥", "达拉斯", "united states", "los angeles", "san jose", "seattle", "new york", "chicago", "dallas", "usa", "us"}},
	{"GB", []string{"英国", "英國", "伦敦", "倫敦", "united kingdom", "london", "uk", "gb"}},
	{"DE", []string{"德国", "德國", "法兰克福", "germany", "frankfurt", "de"}},
	{"FR", []string{"法国", "法國", "巴黎", "france", "paris", "fr"}},
	{"NL", []string{"荷兰", "荷蘭", "阿姆斯特丹", "netherlands", "amsterdam", "nl"}},
	{"RU", []string{"俄罗斯", "俄羅斯", "莫斯科", "russia", "moscow", "ru"}},
	{"CA", []string{"加拿大", "canada", "toronto", "vancouver", "ca"}},
	{"AU", []string{"澳大利亚", "澳洲", "悉尼", "australia", "sydney", "au"}},
	{"IN", []string{"印度", "india", "mumbai"}},
	{"TR", []string{"土耳其", "turkey", "istanbul"}},
	{"AR", []string{"阿根廷", "argentina"}},
	{"BR", []string{"巴西", "brazil"}},
	{"MY", []string{"马来西亚", "馬來西亞", "malaysia"}},
	{"TH", []string{"泰国", "泰國", "thailand", "bangkok"}},
	{"VN", []string{"越南", "vietnam"}},
	{"PH", []string{"菲律宾", "菲律賓", "philippines"}},
	{"ID", []string{"印尼", "印度尼西亚", "indonesia"}},
	{"IT", []string{"意大利", "italy", "milan"}},
	{"ES", []string{"西班牙", "spain", "madrid"}},
	{"SE", []string{"瑞典", "sweden"}},
	{"CH", []string{"瑞士", "switzerland", "zurich"}},
	{"PL", []string{"波兰", "波蘭", "poland"}},
	{"UA", []string{"乌克兰", "烏克蘭", "ukraine"}},
	{"AE", []string{"阿联酋", "迪拜", "dubai", "uae"}},
	{"IL", []string{"以色列", "israel"}},
	{"ZA", []string{"南非", "south africa"}},
	{"NG", []string{"尼日利亚", "nigeria"}},
	{"EG", []string{"埃及", "egypt"}},
	{"MX", []string{"墨西哥", "mexico"}},
	{"CL", []string{"智利", "chile"}},
	{"NZ", []string{"新西兰", "new zealand"}},
	{"CN", []string{"中国", "中國", "大陆", "china", "cn"}},
}
