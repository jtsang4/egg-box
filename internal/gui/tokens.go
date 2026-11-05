package gui

import (
	"github.com/egoist/mygo/ui"
)

// palette is the colour system of the interface. Everything the application
// draws takes its colours from here, so a theme change is one switch.
//
// The brand colour is blue. Surfaces are tinted towards it (a deep navy in the
// dark theme, a cool white in the light one), and status colours are chosen so
// that they stay legible next to it.
type palette struct {
	Dark bool

	// Window is behind everything; Sidebar is the navigation column.
	Window  ui.Color
	Sidebar ui.Color
	// Panel is the face of a card, PanelHi the same while hovered, Raised the
	// face of things that float (menus, dialogs), Field the face of inputs.
	Panel   ui.Color
	PanelHi ui.Color
	Raised  ui.Color
	Field   ui.Color
	// Line is a hairline, LineHi a stronger edge.
	Line   ui.Color
	LineHi ui.Color

	Text  ui.Color // primary
	Text2 ui.Color // secondary
	Text3 ui.Color // tertiary, placeholders

	Blue     ui.Color // the brand colour
	BlueHi   ui.Color // lighter, for highlights and gradients
	BlueLo   ui.Color // deeper, for pressed states and gradients
	BlueText ui.Color // the brand colour as text on a surface
	Cyan     ui.Color
	Indigo   ui.Color

	Good ui.Color
	Warn ui.Color
	Bad  ui.Color

	// Shadow is the colour of drop shadows.
	Shadow ui.Color
	// Glow is the colour of the brand's ambient light.
	Glow ui.Color
}

var darkPalette = palette{
	Dark:    true,
	Window:  ui.Hex("#060A15"),
	Sidebar: ui.Hex("#080D1B"),
	Panel:   ui.RGBA(255, 255, 255, 0.045),
	PanelHi: ui.RGBA(255, 255, 255, 0.08),
	Raised:  ui.Hex("#101830"),
	Field:   ui.RGBA(255, 255, 255, 0.06),
	Line:    ui.RGBA(255, 255, 255, 0.085),
	LineHi:  ui.RGBA(255, 255, 255, 0.16),

	Text:  ui.Hex("#EAF0FF"),
	Text2: ui.Hex("#9DABCB"),
	Text3: ui.Hex("#66759C"),

	Blue:     ui.Hex("#3B7BFF"),
	BlueHi:   ui.Hex("#74A6FF"),
	BlueLo:   ui.Hex("#1F52E0"),
	BlueText: ui.Hex("#86B2FF"),
	Cyan:     ui.Hex("#35C8FF"),
	Indigo:   ui.Hex("#7C8CFF"),

	Good: ui.Hex("#2EDDA6"),
	Warn: ui.Hex("#FFC247"),
	Bad:  ui.Hex("#FF6B7C"),

	Shadow: ui.RGBA(0, 0, 0, 0.45),
	Glow:   ui.RGBA(59, 123, 255, 0.5),
}

var lightPalette = palette{
	Dark:    false,
	Window:  ui.Hex("#EDF2FB"),
	Sidebar: ui.Hex("#E4ECFA"),
	Panel:   ui.Hex("#FFFFFF"),
	PanelHi: ui.Hex("#F6F9FF"),
	Raised:  ui.Hex("#FFFFFF"),
	Field:   ui.Hex("#EEF3FC"),
	Line:    ui.RGBA(16, 40, 96, 0.09),
	LineHi:  ui.RGBA(16, 40, 96, 0.18),

	Text:  ui.Hex("#0B1630"),
	Text2: ui.Hex("#4A5B84"),
	Text3: ui.Hex("#8795B6"),

	Blue:     ui.Hex("#2358F2"),
	BlueHi:   ui.Hex("#4F84FF"),
	BlueLo:   ui.Hex("#1743C8"),
	BlueText: ui.Hex("#1B4DE0"),
	Cyan:     ui.Hex("#0AA5E0"),
	Indigo:   ui.Hex("#5566E6"),

	Good: ui.Hex("#0E9F75"),
	Warn: ui.Hex("#C27F00"),
	Bad:  ui.Hex("#DF3B50"),

	Shadow: ui.RGBA(24, 52, 120, 0.16),
	Glow:   ui.RGBA(35, 88, 242, 0.35),
}

// Metrics shared by every component, in DIPs.
const (
	radiusCard    = 18
	radiusControl = 11
	radiusSmall   = 8
	radiusPill    = 999

	sidebarWidth = 236
	pagePadding  = 28
	cardPadding  = 20
)

// pal returns the palette of the window's appearance.
func (a *App) pal() *palette {
	if a.dark {
		return &darkPalette
	}
	return &lightPalette
}

// themeFor builds the toolkit theme that makes stock widgets (text inputs,
// scroll bars, tooltips, menus) match the palette.
func themeFor(p *palette) *ui.Theme {
	var t ui.Theme
	if p.Dark {
		t = *ui.DarkTheme()
	} else {
		t = *ui.LightTheme()
	}
	t.Dark = p.Dark
	t.Background = p.Window
	t.Surface = p.Field
	t.SurfaceHover = p.PanelHi
	t.SurfacePressed = p.LineHi
	t.Border = p.Line
	t.Text = p.Text
	t.TextMuted = p.Text2
	t.Accent = p.Blue
	t.AccentHover = p.BlueHi
	t.AccentPressed = p.BlueLo
	t.AccentText = ui.Hex("#FFFFFF")
	t.Danger = p.Bad
	t.Warning = p.Warn
	t.Success = p.Good
	t.Selection = p.Blue.Alpha(0.28)
	t.Focus = p.BlueHi.Alpha(0.7)
	t.Scrollbar = p.Text3.Alpha(0.55)
	t.ScrollbarWidth = 6
	t.Radius = radiusControl
	t.Spacing = 4
	if p.Dark {
		t.Inverse, t.InverseText = ui.Hex("#E7EEFF"), ui.Hex("#0A1226")
	} else {
		t.Inverse, t.InverseText = ui.Hex("#0F1B3A"), ui.Hex("#F3F7FF")
	}
	return &t
}
