package winplat

import "strings"

// SameGUID compares two adapter GUIDs the way Windows does: ignoring case and
// the braces.
func SameGUID(a, b string) bool {
	return strings.EqualFold(strings.Trim(a, "{}"), strings.Trim(b, "{}"))
}

// IsWintun reports whether an adapter's driver description is a Wintun
// adapter's: the core names its adapters "sing-tun", Wintun itself "Wintun".
// Nothing else is ever removed by this program.
func IsWintun(description string) bool {
	d := strings.ToLower(description)
	return strings.Contains(d, "wintun") || strings.Contains(d, "sing-tun")
}
