//go:build android && cgo

package androidhost

// MyGo's Android host and sing-box (common/jni, which reads the system's
// certificate store for gomobile builds) both define JNI_OnLoad, and both end
// up in the application's one library. The linker is told to keep the first
// definition, which is MyGo's: its package comes first in the link. MyGo's
// registers the bridge to Kotlin, without which the application shows
// nothing; sing-box's only remembers the VM, and without it sing-box reads
// the system's certificates from their directory instead (x509.SystemCertPool).
// Were the order ever to change, the application would not start at all,
// which every launch shows.

// #cgo LDFLAGS: -Wl,--allow-multiple-definition
import "C"
