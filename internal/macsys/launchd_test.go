package macsys

import (
	"bytes"
	"encoding/xml"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"eggbox/internal/brand"
)

// value is one plist value: a string, a number, a boolean, a list or a dict.
type value struct {
	kind  string
	text  string
	list  []value
	dict  map[string]value
	order []string
}

// parseValue reads the next plist value from the decoder.
func parseValue(t *testing.T, d *xml.Decoder, start xml.StartElement) value {
	t.Helper()
	switch start.Name.Local {
	case "string", "integer", "key":
		var s string
		if err := d.DecodeElement(&s, &start); err != nil {
			t.Fatal(err)
		}
		return value{kind: start.Name.Local, text: s}
	case "true", "false":
		_ = d.Skip()
		return value{kind: "bool", text: start.Name.Local}
	case "array":
		v := value{kind: "array"}
		for {
			tok, err := d.Token()
			if err != nil {
				t.Fatal(err)
			}
			switch e := tok.(type) {
			case xml.StartElement:
				v.list = append(v.list, parseValue(t, d, e))
			case xml.EndElement:
				return v
			}
		}
	case "dict":
		v := value{kind: "dict", dict: map[string]value{}}
		var key string
		for {
			tok, err := d.Token()
			if err != nil {
				t.Fatal(err)
			}
			switch e := tok.(type) {
			case xml.StartElement:
				if e.Name.Local == "key" {
					key = parseValue(t, d, e).text
					v.order = append(v.order, key)
					continue
				}
				v.dict[key] = parseValue(t, d, e)
			case xml.EndElement:
				return v
			}
		}
	}
	t.Fatalf("unexpected element %s", start.Name.Local)
	return value{}
}

func parsePlist(t *testing.T, raw []byte) value {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(raw))
	d.Strict = true
	d.Entity = xml.HTMLEntity
	for {
		tok, err := d.Token()
		if err == io.EOF {
			t.Fatal("no dict in the plist")
		}
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := tok.(xml.StartElement); ok && e.Name.Local == "dict" {
			return parseValue(t, d, e)
		}
	}
}

func TestDaemonPlist(t *testing.T) {
	root := parsePlist(t, DaemonPlistXML()).dict

	if root["Label"].text != brand.AgentLabel {
		t.Errorf("Label = %q", root["Label"].text)
	}
	if root["BundleProgram"].text != "Contents/MacOS/agentd" {
		t.Errorf("BundleProgram = %q", root["BundleProgram"].text)
	}
	args := root["ProgramArguments"].list
	if len(args) != 2 || args[0].text != "agentd" || args[1].text != "daemon" {
		t.Errorf("ProgramArguments = %+v", args)
	}
	// The Mach service must be named as the agent listens on it.
	mach := root["MachServices"].dict
	if v, ok := mach[brand.AgentLabel]; !ok || v.text != "true" {
		t.Errorf("MachServices = %+v", mach)
	}
	if got := root["AssociatedBundleIdentifiers"].list; len(got) != 1 || got[0].text != brand.AppID {
		t.Errorf("AssociatedBundleIdentifiers = %+v", got)
	}
	if root["RunAtLoad"].text != "true" {
		t.Error("the service must start with the computer once it is allowed")
	}
	if ka := root["KeepAlive"].dict["SuccessfulExit"]; ka.text != "false" {
		t.Errorf("KeepAlive = %+v: a clean stop must stay stopped, a crash must restart", root["KeepAlive"])
	}
	if root["ExitTimeOut"].text != "25" || root["ThrottleInterval"].text != "30" {
		t.Errorf("timeouts: exit %q throttle %q", root["ExitTimeOut"].text, root["ThrottleInterval"].text)
	}
	if root["StandardErrorPath"].text != LogPath || root["StandardOutPath"].text != LogPath {
		t.Error("the log path is not set on both streams")
	}
}

func TestDaemonPlistPassesPlutil(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil is not available here")
	}
	path := filepath.Join(t.TempDir(), DaemonPlist)
	if err := writeFile(path, DaemonPlistXML()); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v\n%s", err, out)
	}
}

func TestLaunchdWaitsLongerThanTheServiceNeeds(t *testing.T) {
	exit := parsePlist(t, DaemonPlistXML()).dict["ExitTimeOut"].text
	if want := strconv.Itoa(int(ExitTimeOut / time.Second)); exit != want {
		t.Errorf("ExitTimeOut = %s, want %s", exit, want)
	}
	if ExitTimeOut <= ShutdownBudget {
		t.Errorf("launchd (%v) must wait longer than the service's own budget (%v)", ExitTimeOut, ShutdownBudget)
	}
}
