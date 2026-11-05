package gui

import (
	"slices"
	"sync"

	"github.com/egoist/mygo/ui"
)

// The icon set is drawn on a 24×24 grid with a 1.75 stroke, round caps and
// joins, in the manner of the common line-icon sets, and is kept as data here
// so that the whole interface stays Go: no icon file ever leaves the binary.
const (
	svgHead = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">`
	svgFoot = `</svg>`
)

// iconData holds the inner markup of every icon by name.
var iconData = map[string]string{
	"power":          `<path d="M12 3v8.5"/><path d="M17.7 6.6a8.2 8.2 0 1 1-11.4 0"/>`,
	"home":           `<path d="M3.5 10.8 12 3.6l8.5 7.2"/><path d="M5.5 9.6V19a1.5 1.5 0 0 0 1.5 1.5h3v-5.5h4v5.5h3a1.5 1.5 0 0 0 1.5-1.5V9.6"/>`,
	"globe":          `<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.6 2.5 4 5.6 4 9s-1.4 6.5-4 9c-2.6-2.5-4-5.6-4-9s1.4-6.5 4-9Z"/>`,
	"layers":         `<path d="m12 3.2 8.8 4.8-8.8 4.8L3.2 8 12 3.2Z"/><path d="m3.2 12.2 8.8 4.8 8.8-4.8"/><path d="m3.2 16.4 8.8 4.8 8.8-4.8"/>`,
	"rss":            `<path d="M5 11a8 8 0 0 1 8 8"/><path d="M5 4.5A14.5 14.5 0 0 1 19.5 19"/><circle cx="5.8" cy="18.2" r="1.2"/>`,
	"route":          `<circle cx="6" cy="19" r="2.2"/><circle cx="18" cy="5" r="2.2"/><path d="M8.2 19h8a3.5 3.5 0 0 0 0-7h-9a3.5 3.5 0 0 1 0-7h8.6"/>`,
	"split":          `<path d="M16 3h5v5"/><path d="M8 3H3v5"/><path d="M12 22v-8.3a4 4 0 0 0-1.2-2.8L3 3"/><path d="m15 9 6-6"/>`,
	"activity":       `<path d="M21.5 12h-4l-3 8.5L9.5 3.5 6.5 12h-4"/>`,
	"terminal":       `<path d="m5 7 5 5-5 5"/><path d="M12.5 18h7"/>`,
	"settings":       `<path d="M12.2 2.8h-.4a2 2 0 0 0-2 2v.2a2 2 0 0 1-1 1.7l-.4.3a2 2 0 0 1-2 0l-.2-.1a2 2 0 0 0-2.7.7l-.2.4a2 2 0 0 0 .7 2.7l.2.1a2 2 0 0 1 1 1.7v.5a2 2 0 0 1-1 1.8l-.2.1a2 2 0 0 0-.7 2.7l.2.4a2 2 0 0 0 2.7.7l.2-.1a2 2 0 0 1 2 0l.4.3a2 2 0 0 1 1 1.7v.2a2 2 0 0 0 2 2h.4a2 2 0 0 0 2-2v-.2a2 2 0 0 1 1-1.7l.4-.3a2 2 0 0 1 2 0l.2.1a2 2 0 0 0 2.7-.7l.2-.4a2 2 0 0 0-.7-2.7l-.2-.1a2 2 0 0 1-1-1.8v-.5a2 2 0 0 1 1-1.7l.2-.1a2 2 0 0 0 .7-2.7l-.2-.4a2 2 0 0 0-2.7-.7l-.2.1a2 2 0 0 1-2 0l-.4-.3a2 2 0 0 1-1-1.7v-.2a2 2 0 0 0-2-2Z"/><circle cx="12" cy="12" r="3"/>`,
	"shield":         `<path d="M12 21.5s7.5-3.6 7.5-9.5V5.3L12 2.5 4.5 5.3V12c0 5.9 7.5 9.5 7.5 9.5Z"/>`,
	"shield-check":   `<path d="M12 21.5s7.5-3.6 7.5-9.5V5.3L12 2.5 4.5 5.3V12c0 5.9 7.5 9.5 7.5 9.5Z"/><path d="m8.8 12 2.3 2.3 4.2-4.4"/>`,
	"shield-off":     `<path d="M12 21.5s7.5-3.6 7.5-9.5V5.3L12 2.5 4.5 5.3V12c0 5.9 7.5 9.5 7.5 9.5Z" opacity=".55"/><path d="m3 3 18 18"/>`,
	"zap":            `<path d="M13.2 2.5 4 13.8h7.3l-.9 7.7 9.6-11.6h-7.4l.6-7.4Z"/>`,
	"refresh":        `<path d="M20 12a8 8 0 0 1-13.7 5.6L4 15.3"/><path d="M4 12a8 8 0 0 1 13.7-5.6L20 8.7"/><path d="M20 4v4.7h-4.7"/><path d="M4 20v-4.7h4.7"/>`,
	"plus":           `<path d="M12 5v14"/><path d="M5 12h14"/>`,
	"minus":          `<path d="M5 12h14"/>`,
	"trash":          `<path d="M4 6.5h16"/><path d="M18.5 6.5V19a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2V6.5"/><path d="M8.5 6.5V5a2 2 0 0 1 2-2h3a2 2 0 0 1 2 2v1.5"/><path d="M10 11v6"/><path d="M14 11v6"/>`,
	"pencil":         `<path d="M16.6 3.9a2.4 2.4 0 0 1 3.4 3.4L7.8 19.5 3 21l1.5-4.8L16.6 3.9Z"/><path d="m14.5 6 3.5 3.5"/>`,
	"copy":           `<rect x="8.5" y="8.5" width="12" height="12" rx="2.2"/><path d="M15.5 8.5V6.2a2.2 2.2 0 0 0-2.2-2.2H6.2A2.2 2.2 0 0 0 4 6.2v7.1a2.2 2.2 0 0 0 2.2 2.2h2.3"/>`,
	"check":          `<path d="m5 12.5 4.5 4.5L19 7.5"/>`,
	"x":              `<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`,
	"chevron-down":   `<path d="m6.5 9.5 5.5 5.5 5.5-5.5"/>`,
	"chevron-up":     `<path d="m6.5 14.5 5.5-5.5 5.5 5.5"/>`,
	"chevron-right":  `<path d="m9.5 6.5 5.5 5.5-5.5 5.5"/>`,
	"chevron-left":   `<path d="m14.5 6.5-5.5 5.5 5.5 5.5"/>`,
	"search":         `<circle cx="11" cy="11" r="7.2"/><path d="m20.5 20.5-4.3-4.3"/>`,
	"arrow-up":       `<path d="M12 19.5v-15"/><path d="m5.5 11 6.5-6.5 6.5 6.5"/>`,
	"arrow-down":     `<path d="M12 4.5v15"/><path d="m5.5 13 6.5 6.5 6.5-6.5"/>`,
	"arrow-right":    `<path d="M4.5 12h15"/><path d="m13 5.5 6.5 6.5-6.5 6.5"/>`,
	"clock":          `<circle cx="12" cy="12" r="9"/><path d="M12 6.8V12l3.4 2"/>`,
	"download":       `<path d="M20.5 15v3.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2V15"/><path d="m7.5 10.5 4.5 4.5 4.5-4.5"/><path d="M12 15V3.5"/>`,
	"upload":         `<path d="M20.5 15v3.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2V15"/><path d="m16.5 8-4.5-4.5L7.5 8"/><path d="M12 3.5V15"/>`,
	"link":           `<path d="M10 13.6a4.6 4.6 0 0 0 6.9.5l2.8-2.8a4.6 4.6 0 0 0-6.5-6.5l-1.6 1.6"/><path d="M14 10.4a4.6 4.6 0 0 0-6.9-.5l-2.8 2.8a4.6 4.6 0 0 0 6.5 6.5l1.6-1.6"/>`,
	"file-text":      `<path d="M14.5 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V7.5L14.5 3Z"/><path d="M14 3v5h5"/><path d="M9 13h6"/><path d="M9 17h6"/>`,
	"moon":           `<path d="M20.5 14.2A8.5 8.5 0 1 1 9.8 3.5a6.7 6.7 0 0 0 10.7 10.7Z"/>`,
	"sun":            `<circle cx="12" cy="12" r="4"/><path d="M12 2.5v2"/><path d="M12 19.5v2"/><path d="m4.9 4.9 1.4 1.4"/><path d="m17.7 17.7 1.4 1.4"/><path d="M2.5 12h2"/><path d="M19.5 12h2"/><path d="m4.9 19.1 1.4-1.4"/><path d="m17.7 6.3 1.4-1.4"/>`,
	"monitor":        `<rect x="2.8" y="3.8" width="18.4" height="12.4" rx="2.2"/><path d="M8.5 20.5h7"/><path d="M12 16.2v4.3"/>`,
	"info":           `<circle cx="12" cy="12" r="9"/><path d="M12 16.5V11.5"/><path d="M12 8h.01"/>`,
	"alert":          `<path d="M10.3 4.2 2.7 17.5A2 2 0 0 0 4.4 20.5h15.2a2 2 0 0 0 1.7-3L13.7 4.2a2 2 0 0 0-3.4 0Z"/><path d="M12 9.5v4"/><path d="M12 17h.01"/>`,
	"alert-circle":   `<circle cx="12" cy="12" r="9"/><path d="M12 7.5v5"/><path d="M12 16.2h.01"/>`,
	"check-circle":   `<circle cx="12" cy="12" r="9"/><path d="m8.2 12.3 2.6 2.6 5-5.3"/>`,
	"x-circle":       `<circle cx="12" cy="12" r="9"/><path d="m9 9 6 6"/><path d="m15 9-6 6"/>`,
	"lock":           `<rect x="4" y="10.5" width="16" height="10.5" rx="2.4"/><path d="M7.5 10.5V7.5a4.5 4.5 0 0 1 9 0v3"/><path d="M12 15v2.2"/>`,
	"server":         `<rect x="3" y="3.5" width="18" height="7.5" rx="2.2"/><rect x="3" y="13" width="18" height="7.5" rx="2.2"/><path d="M7 7.25h.01"/><path d="M7 16.75h.01"/>`,
	"wifi":           `<path d="M5 12.6a10 10 0 0 1 14 0"/><path d="M1.8 9.2a14.5 14.5 0 0 1 20.4 0"/><path d="M8.5 16a5.2 5.2 0 0 1 7 0"/><path d="M12 19.6h.01"/>`,
	"play":           `<path d="M7 4.6v14.8a1 1 0 0 0 1.5.9l12-7.4a1 1 0 0 0 0-1.8l-12-7.4A1 1 0 0 0 7 4.6Z"/>`,
	"pause":          `<rect x="6" y="4.5" width="4" height="15" rx="1.2"/><rect x="14" y="4.5" width="4" height="15" rx="1.2"/>`,
	"filter":         `<path d="M3.5 5h17l-6.5 7.6V19l-4 2v-8.4L3.5 5Z"/>`,
	"list":           `<path d="M8.5 6h12"/><path d="M8.5 12h12"/><path d="M8.5 18h12"/><path d="M3.8 6h.01"/><path d="M3.8 12h.01"/><path d="M3.8 18h.01"/>`,
	"grid":           `<rect x="3.5" y="3.5" width="7" height="7" rx="1.8"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.8"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.8"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.8"/>`,
	"more":           `<circle cx="5.5" cy="12" r="1.1"/><circle cx="12" cy="12" r="1.1"/><circle cx="18.5" cy="12" r="1.1"/>`,
	"external":       `<path d="M18.5 13.5V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7.5a2 2 0 0 1 2-2h5.5"/><path d="M15 3h6v6"/><path d="m10 14 11-11"/>`,
	"eye":            `<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12Z"/><circle cx="12" cy="12" r="2.8"/>`,
	"bell":           `<path d="M6 8.5a6 6 0 0 1 12 0c0 6.5 2.8 8.5 2.8 8.5H3.2S6 15 6 8.5Z"/><path d="M10.2 20.5a2 2 0 0 0 3.6 0"/>`,
	"sparkles":       `<path d="m12 3-1.9 5.7a2 2 0 0 1-1.3 1.3L3 12l5.8 2a2 2 0 0 1 1.3 1.3L12 21l1.9-5.7a2 2 0 0 1 1.3-1.3L21 12l-5.8-2a2 2 0 0 1-1.3-1.3L12 3Z"/>`,
	"database":       `<ellipse cx="12" cy="5.5" rx="8.5" ry="3"/><path d="M3.5 5.5v13c0 1.7 3.8 3 8.5 3s8.5-1.3 8.5-3v-13"/><path d="M3.5 12c0 1.7 3.8 3 8.5 3s8.5-1.3 8.5-3"/>`,
	"sliders":        `<path d="M4 21v-7"/><path d="M4 10V3"/><path d="M12 21v-9"/><path d="M12 8V3"/><path d="M20 21v-5"/><path d="M20 12V3"/><path d="M1.5 14h5"/><path d="M9.5 8h5"/><path d="M17.5 16h5"/>`,
	"swap":           `<path d="m7 3.5-3.5 3.5L7 10.5"/><path d="M3.5 7H20"/><path d="m17 13.5 3.5 3.5-3.5 3.5"/><path d="M20.5 17H4"/>`,
	"gauge":          `<path d="m12 14 3.8-3.8"/><path d="M3.8 19a9.5 9.5 0 1 1 16.4 0"/>`,
	"cpu":            `<rect x="5" y="5" width="14" height="14" rx="2.2"/><rect x="9" y="9" width="6" height="6" rx="1"/><path d="M9 2.5v2.5"/><path d="M15 2.5v2.5"/><path d="M9 19v2.5"/><path d="M15 19v2.5"/><path d="M2.5 9H5"/><path d="M2.5 15H5"/><path d="M19 9h2.5"/><path d="M19 15h2.5"/>`,
	"life-buoy":      `<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="3.6"/><path d="m5.6 5.6 3.8 3.8"/><path d="m14.6 14.6 3.8 3.8"/><path d="m14.6 9.4 3.8-3.8"/><path d="m5.6 18.4 3.8-3.8"/>`,
	"key":            `<circle cx="7.8" cy="15.8" r="4.3"/><path d="m10.9 12.7 9.6-9.6"/><path d="m16.5 7.1 3 3"/><path d="m14 9.6 2.2 2.2"/>`,
	"cloud":          `<path d="M7 19a5 5 0 0 1-.7-9.95A6.5 6.5 0 0 1 18.9 9.6 4.7 4.7 0 0 1 17.5 19H7Z"/>`,
	"inbox":          `<path d="M3.5 13.5h5l1.5 2.5h4l1.5-2.5h5"/><path d="M6.4 4.5h11.2a2 2 0 0 1 1.8 1.2l2.1 6.9V18a2 2 0 0 1-2 2H4.5a2 2 0 0 1-2-2v-5.4l2.1-6.9a2 2 0 0 1 1.8-1.2Z"/>`,
	"arrow-up-right": `<path d="M7 17 17 7"/><path d="M8 7h9v9"/>`,
	"rotate":         `<path d="M3.5 12a8.5 8.5 0 1 0 2.9-6.4L3.5 8.5"/><path d="M3.5 3.5v5h5"/>`,
	"bolt-slash":     `<path d="m3 3 18 18"/><path d="M13.2 2.5 8.2 8.6"/><path d="M14.6 12.4 11.3 21.5 13 13.8"/><path d="M4 13.8h7.3"/>`,
	"command":        `<path d="M9 9V6.5A2.5 2.5 0 1 0 6.5 9H9Zm0 0h6m-6 0v6m6-6V6.5A2.5 2.5 0 1 1 17.5 9H15Zm0 0v6m0 0h2.5a2.5 2.5 0 1 1-2.5 2.5V15Zm0 0H9m0 0H6.5A2.5 2.5 0 1 0 9 17.5V15Z"/>`,
	"edit-rule":      `<path d="M4 6h10"/><path d="M4 12h6"/><path d="M4 18h7"/><path d="m14.5 17 5-5a1.9 1.9 0 0 0-2.7-2.7l-5 5L11 18l3.5-1Z"/>`,
	"heart":          `<path d="M12 20.5s-8-4.9-8-11A4.5 4.5 0 0 1 12 6.8a4.5 4.5 0 0 1 8 2.7c0 6.1-8 11-8 11Z"/>`,
}

var (
	iconMu    sync.Mutex
	iconCache = map[string]*ui.SVG{}
)

// icon returns the named icon, parsed once.
func icon(name string) *ui.SVG {
	iconMu.Lock()
	defer iconMu.Unlock()
	if s, ok := iconCache[name]; ok {
		return s
	}
	inner, ok := iconData[name]
	if !ok {
		inner = iconData["info"]
	}
	s := ui.MustParseSVG([]byte(svgHead + inner + svgFoot))
	iconCache[name] = s
	return s
}

// iconNames lists every icon, for the icon sheet.
func iconNames() []string {
	names := make([]string, 0, len(iconData))
	for k := range iconData {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// mustSVG parses an SVG that is part of the program.
func mustSVG(src string) *ui.SVG { return ui.MustParseSVG([]byte(src)) }
