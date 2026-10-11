package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

// launchdPlist is the LaunchAgent that starts Forge Gateway at login on a Mac. Forge Gateway's
// README installs it from this path on forge-solo's main branch, with one sed that puts the user's
// home folder in place of launchdHome: launchd expands neither ~ nor $HOME.
const (
	launchdPlist = "org.bitcoincashii.forge-gateway.plist"
	launchdHome  = "/Users/YOU"
)

// plistValue is the property list value that start opens, read to its end: a string, a bool, a
// []any (array) or a map[string]any (dict). Anything else fails the test: the LaunchAgent needs
// nothing else.
func plistValue(t *testing.T, d *xml.Decoder, start xml.StartElement) any {
	t.Helper()
	switch start.Name.Local {
	case "string", "key":
		var s string
		if err := d.DecodeElement(&s, &start); err != nil {
			t.Fatalf("GW-LAUNCHD-XML: <%s>: %v", start.Name.Local, err)
		}
		return s
	case "true", "false":
		var empty struct {
			Inner string `xml:",innerxml"`
		}
		if err := d.DecodeElement(&empty, &start); err != nil || empty.Inner != "" {
			t.Fatalf("GW-LAUNCHD-XML: <%s> is not empty (%q, %v)", start.Name.Local, empty.Inner, err)
		}
		return start.Name.Local == "true"
	case "array":
		list := []any{}
		for {
			el, end := plistNext(t, d)
			if end {
				return list
			}
			list = append(list, plistValue(t, d, el))
		}
	case "dict":
		dict := map[string]any{}
		for {
			el, end := plistNext(t, d)
			if end {
				return dict
			}
			if el.Name.Local != "key" {
				t.Fatalf("GW-LAUNCHD-XML: a dict holds <%s> where a <key> belongs", el.Name.Local)
			}
			key := plistValue(t, d, el).(string)
			val, end := plistNext(t, d)
			if end {
				t.Fatalf("GW-LAUNCHD-XML: the key %s has no value", key)
			}
			if _, dup := dict[key]; dup {
				t.Fatalf("GW-LAUNCHD-XML: the key %s is there twice", key)
			}
			dict[key] = plistValue(t, d, val)
		}
	}
	t.Fatalf("GW-LAUNCHD-XML: <%s> is not a value the LaunchAgent uses", start.Name.Local)
	return nil
}

// plistNext is the next element inside the one being read, or end=true at that one's end. Only
// white space may come between elements.
func plistNext(t *testing.T, d *xml.Decoder) (el xml.StartElement, end bool) {
	t.Helper()
	for {
		tok, err := d.Token()
		if err != nil {
			t.Fatalf("GW-LAUNCHD-XML: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			return tok, false
		case xml.EndElement:
			return xml.StartElement{}, true
		case xml.CharData:
			if len(bytes.TrimSpace(tok)) != 0 {
				t.Fatalf("GW-LAUNCHD-XML: text %q between elements", tok)
			}
		case xml.Comment:
		default:
			t.Fatalf("GW-LAUNCHD-XML: %T inside the plist", tok)
		}
	}
}

// The LaunchAgent is an XML property list in Apple's form (plutil -lint checks it on a Mac, in
// forge-gateway's macOS CI), named for its label as launchd expects. It runs the program and the
// config the README puts in ~/ForgeGateway, at login and again whenever it stops (as
// forge-gateway.service's Restart=always), logs to ~/Library/Logs/ForgeGateway, and has nothing
// else. Every path starts with the home folder the README's sed replaces.
func TestLaunchAgentPlist(t *testing.T) {
	raw, err := os.ReadFile(launchdPlist)
	if err != nil {
		t.Fatal(err)
	}
	const prolog = `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n"
	if !bytes.HasPrefix(raw, []byte(prolog)) {
		t.Errorf("GW-LAUNCHD-PROLOG: the plist does not start with Apple's XML declaration and plist DOCTYPE:\n%s", prolog)
	}
	if bytes.Contains(raw, []byte("\r")) {
		t.Error("GW-LAUNCHD-CRLF: the plist has Windows line ends")
	}

	d := xml.NewDecoder(bytes.NewReader(raw))
	d.Strict = true
	var root *xml.StartElement
	var dict any
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("GW-LAUNCHD-XML: %v", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			if cd, text := tok.(xml.CharData); text && len(bytes.TrimSpace(cd)) != 0 {
				t.Fatalf("GW-LAUNCHD-XML: text %q outside <plist>", cd)
			}
			continue
		}
		if root != nil {
			t.Fatalf("GW-LAUNCHD-XML: <%s> after </plist>", el.Name.Local)
		}
		root = &el
		if el.Name.Local != "plist" || len(el.Attr) != 1 || el.Attr[0].Name.Local != "version" || el.Attr[0].Value != "1.0" {
			t.Fatalf(`GW-LAUNCHD-XML: the root is <%s %v>, not <plist version="1.0">`, el.Name.Local, el.Attr)
		}
		top, end := plistNext(t, d)
		if end || top.Name.Local != "dict" {
			t.Fatal("GW-LAUNCHD-XML: <plist> does not hold a <dict>")
		}
		dict = plistValue(t, d, top)
		if _, end := plistNext(t, d); !end {
			t.Fatal("GW-LAUNCHD-XML: <plist> holds more than its <dict>")
		}
	}

	want := map[string]any{
		"Label": strings.TrimSuffix(launchdPlist, ".plist"),
		"ProgramArguments": []any{
			launchdHome + "/ForgeGateway/forge-gateway",
			"-config",
			launchdHome + "/ForgeGateway/forge-gateway.json",
		},
		"RunAtLoad":         true,
		"KeepAlive":         true,
		"StandardOutPath":   launchdHome + "/Library/Logs/ForgeGateway/forge-gateway.log",
		"StandardErrorPath": launchdHome + "/Library/Logs/ForgeGateway/forge-gateway.log",
	}
	if !reflect.DeepEqual(dict, want) {
		t.Errorf("GW-LAUNCHD: the LaunchAgent is\n%#v\nwant\n%#v", dict, want)
	}

	// -config is the gateway's own flag for its config file.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte(`fs.String("config", `)) {
		t.Error("GW-LAUNCHD-FLAG: the gateway no longer has a -config flag, which the LaunchAgent passes")
	}
}
