package winsys

import "strings"

// UnderAny reports whether path is inside one of the directories, comparing
// the way Windows does: either slash, case-insensitively, with "." and ".."
// resolved first. A directory is not inside itself; the path must name
// something below it.
//
// It works on the text alone and on any operating system, so it can be tested
// everywhere; it is not a check of what is on disk.
func UnderAny(path string, dirs []string) bool {
	p := segments(path)
	for _, d := range dirs {
		dir := segments(d)
		if len(dir) == 0 || len(p) <= len(dir) {
			continue
		}
		inside := true
		for i := range dir {
			if p[i] != dir[i] {
				inside = false
				break
			}
		}
		if inside {
			return true
		}
	}
	return false
}

// segments splits a Windows path into its lower-cased parts, resolving "."
// and "..". A ".." that would climb above the root stays at the root.
func segments(path string) []string {
	var out []string
	for _, s := range strings.Split(strings.ReplaceAll(path, "/", `\`), `\`) {
		switch s {
		case "", ".":
		case "..":
			if len(out) > 1 { // keep the drive
				out = out[:len(out)-1]
			}
		default:
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}
