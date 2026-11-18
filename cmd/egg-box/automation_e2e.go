//go:build e2e

package main

// End-to-end runs drive the built application through MyGo's automation
// socket (MYGO_AUTOMATION). Release builds leave it out: it is linked only
// with the e2e tag, and only builds with the inspector listen.
import _ "github.com/egoist/mygo/plugins/automation"
