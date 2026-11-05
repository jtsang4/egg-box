package gui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var byteUnits = [...]string{"B", "KB", "MB", "GB", "TB", "PB"}

// splitBytes writes a byte count as a number and its unit: "1.24", "GB".
func splitBytes(n float64) (num, unit string) {
	if n < 0 {
		n = 0
	}
	i := 0
	for n >= 1024 && i < len(byteUnits)-1 {
		n /= 1024
		i++
	}
	switch {
	case i == 0:
		return strconv.FormatFloat(n, 'f', 0, 64), byteUnits[i]
	case n >= 100:
		return strconv.FormatFloat(n, 'f', 0, 64), byteUnits[i]
	case n >= 10:
		return strconv.FormatFloat(n, 'f', 1, 64), byteUnits[i]
	}
	return strconv.FormatFloat(n, 'f', 2, 64), byteUnits[i]
}

// fmtBytes writes a byte count: "1.24 GB".
func fmtBytes(n float64) string {
	num, unit := splitBytes(n)
	return num + " " + unit
}

// fmtRate writes a throughput in bytes per second: "3.4 MB/s".
func fmtRate(bps float64) string {
	num, unit := splitBytes(bps)
	return num + " " + unit + "/s"
}

// splitRate is fmtRate in two parts, for large figures with small units.
func splitRate(bps float64) (num, unit string) {
	num, unit = splitBytes(bps)
	return num, unit + "/s"
}

// fmtCount writes an integer with thousands separators.
func fmtCount(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) > 3 {
		var b strings.Builder
		pre := len(s) % 3
		if pre > 0 {
			b.WriteString(s[:pre])
		}
		for i := pre; i < len(s); i += 3 {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(s[i : i+3])
		}
		s = b.String()
	}
	if neg {
		return "-" + s
	}
	return s
}

// fmtClock writes a duration as H:MM:SS (or M:SS under an hour).
func fmtClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	h, m, sec := s/3600, s/60%60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

// fmtMillis writes a latency: "86 ms".
func fmtMillis(ms int) string {
	return strconv.Itoa(ms) + " ms"
}

// fmtPercent writes a fraction as a percentage.
func fmtPercent(f float64) string {
	if math.IsNaN(f) || f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return strconv.Itoa(int(math.Round(f*100))) + "%"
}

// clamp01 holds v in [0, 1].
func clamp01(v float64) float64 {
	switch {
	case v < 0 || math.IsNaN(v):
		return 0
	case v > 1:
		return 1
	}
	return v
}

// lerp is the linear interpolation from a to b.
func lerp(a, b, t float32) float32 { return a + (b-a)*t }

// smoothstep eases t in [0, 1].
func smoothstep(t float32) float32 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3 - 2*t)
}

// initials returns up to two letters for a name's avatar.
func initials(name string) string {
	var out []rune
	for _, f := range strings.Fields(name) {
		for _, r := range f {
			out = append(out, []rune(strings.ToUpper(string(r)))...)
			break
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "·"
	}
	return string(out)
}

// truncate shortens s to at most n runes with an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// fmtDuration is a short span: 8s, 4m 03s, 2h 15m, 3d.
func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
