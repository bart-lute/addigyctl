package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// appsFixture: d2 (Finance) and d3, d4 (Laptops, below Finance) and d5
// (Sales) have apps; d3 has two versions of Example Editor.
var appsFixture = map[string]string{
	"d2": `[{"agentid":"d2","name":"Example Browser","version":"10.2","path":"/Applications/Example Browser.app"},
	        {"agentid":"d2","name":"Calculator","version":"11.0","path":"/System/Applications/Calculator.app"}]`,
	"d3": `[{"agentid":"d3","name":"Example Browser","version":"10.10","path":"/Applications/Example Browser.app"},
	        {"agentid":"d3","name":"Example Editor","version":"2.0","path":"/Applications/Example Editor.app"},
	        {"agentid":"d3","name":"Example Editor","version":"1.9","path":"/Applications/Old/Example Editor.app"}]`,
	"d4": `[{"agentid":"d4","name":"Example Browser","version":"10.10","path":"/Applications/Example Browser.app"}]`,
	"d5": `[{"agentid":"d5","name":"Example Chat","version":"3.1","path":"/Applications/Example Chat.app"}]`,
}

func runApps(t *testing.T, g Globals, cmd *DevicesAppsCmd) policiesRun {
	t.Helper()
	ds := newDeviceServer(t)
	ds.apps = appsFixture
	if cmd.Sort == "" {
		cmd.Sort = "name"
	}
	return runPoliciesOnServer(t, ds, policiesFixture, g, cmd.Run)
}

func TestDevicesAppsSummarizesAPolicy(t *testing.T) {
	r := runApps(t, Globals{Output: "csv"}, &DevicesAppsCmd{Policy: "Finance"})
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := map[string][]string{}
	recs := parseCSV(t, r.out)
	if strings.Join(recs[0], ",") != "APP,DEVICES,VERSIONS" {
		t.Errorf("header: %v", recs[0])
	}
	for _, rec := range recs[1:] {
		got[rec[0]] = rec[1:]
	}
	if b := got["Example Browser"]; len(b) != 2 || b[0] != "3" || b[1] != "10.10 (2), 10.2 (1)" {
		t.Errorf("Example Browser = %v, want 3 devices, newest version first", b)
	}
	if e := got["Example Editor"]; len(e) != 2 || e[0] != "1" || e[1] != "2.0 (1), 1.9 (1)" {
		t.Errorf("Example Editor = %v, want 1 device with two versions", e)
	}
	if _, ok := got["Calculator"]; ok {
		t.Error("apps under /System should be left out by default")
	}
	if _, ok := got["Example Chat"]; ok {
		t.Error("Sales is not below Finance")
	}
}

func TestDevicesAppsTableAndFlags(t *testing.T) {
	r := runApps(t, Globals{}, &DevicesAppsCmd{Policy: "Finance", System: true})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.out, "Calculator") || strings.Contains(r.out, "--system includes them") {
		t.Errorf("--system should include /System apps and drop the hint:\n%s", r.out)
	}
	if !strings.Contains(r.out, `3 apps on 3 of 3 devices in "Finance" and 1 sub-policy`) {
		t.Errorf("footer:\n%s", r.out)
	}

	// Every device, most common first; d1 and d6 report no apps.
	r = runApps(t, Globals{}, &DevicesAppsCmd{Sort: "devices"})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Index(r.out, "Example Browser") > strings.Index(r.out, "Example Chat") {
		t.Errorf("--sort devices should put the most common app first:\n%s", r.out)
	}
	if !strings.Contains(r.out, "3 apps on 4 of 7 devices") || !strings.Contains(r.out, "--system includes them") {
		t.Errorf("footer:\n%s", r.out)
	}

	for _, cmd := range []*DevicesAppsCmd{{Direct: true}, {Sort: "size"}, {Policy: "no such policy"}} {
		if r := runApps(t, Globals{}, cmd); r.err == nil {
			t.Errorf("%+v should fail", cmd)
		}
	}
}

func TestDevicesAppsJSON(t *testing.T) {
	r := runApps(t, Globals{JSON: true}, &DevicesAppsCmd{Policy: "Finance"})
	if r.err != nil {
		t.Fatal(r.err)
	}
	var got []appSummary
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "Example Editor" || len(got[1].Paths) != 2 || got[1].Versions[0] != (versionSummary{"2.0", 1}) {
		t.Errorf("JSON = %+v", got)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"10.10", "10.2", 1},
		{"1.0", "1.0", 0},
		{"1.0", "1.0.1", -1},
		{"7.1.9 (88375)", "6.5.0 (57940)", 1},
		{"6.4.6-3", "6.4.6-2", 1},
	} {
		if got := compareVersions(tc.a, tc.b); (got > 0) != (tc.want > 0) || (got < 0) != (tc.want < 0) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign of %d", tc.a, tc.b, got, tc.want)
		}
	}
}
