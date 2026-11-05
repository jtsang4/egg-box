//go:build windows

package fsutil

// syncDir is a no-op on Windows: NTFS journals the rename, and directories
// cannot be opened for flushing with the portable API.
func syncDir(string) error { return nil }

func isChmodUnsupported(error) bool { return true }
