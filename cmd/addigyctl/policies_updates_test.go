package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// updatesFixture: Finance has macOS and iOS updates enabled, Laptops (below
// Finance) macOS disabled but stored, Sales macOS with deferrals.
var updatesFixture = map[string]string{
	"finance": `{"policy_id":"finance","last_updated":"2026-10-01T10:00:00Z",
	  "macos_settings":{"enabled":true,"update_mode":"max_version","max_os_version_allowed":"27.9.99","days_after_release":7,"keep_os_updated":false,"allowed_days":["Mon","Tue"]},
	  "ios_settings":{"enabled":true,"update_mode":"latest","max_os_version_allowed":"15.6","days_after_release":3,"keep_os_updated":true},
	  "ipados_settings":{"enabled":false},"tvos_settings":{"enabled":false},"schedule":{"enabled":false}}`,
	"fin-laptops": `{"policy_id":"fin-laptops","last_updated":null,
	  "macos_settings":{"enabled":false,"update_mode":"max_version","max_os_version_allowed":"13.9.9"},
	  "ios_settings":{"enabled":false},"ipados_settings":{"enabled":false},"tvos_settings":{"enabled":false},"schedule":{"enabled":false}}`,
	"sales": `{"policy_id":"sales","last_updated":"2026-10-02T10:00:00Z",
	  "macos_settings":{"enabled":true,"update_mode":"max_version","max_os_version_allowed":"26.9.99","max_user_deferrals":3,
	    "deferrals":{"major_period_in_days":30,"minor_period_in_days":7,"system_period_in_days":0}},
	  "ios_settings":{"enabled":false},"ipados_settings":{"enabled":false},"tvos_settings":{"enabled":false},"schedule":{"enabled":false}}`,
}

func runUpdates(t *testing.T, g Globals, cmd *PoliciesUpdatesCmd) policiesRun {
	t.Helper()
	ds := newDeviceServer(t)
	ds.updates = updatesFixture
	return runPoliciesOnServer(t, ds, policiesFixture, g, cmd.Run)
}

func TestPoliciesUpdatesShowsEnabledSettings(t *testing.T) {
	r := runUpdates(t, Globals{}, &PoliciesUpdatesCmd{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	lines := strings.Split(r.out, "\n")
	var finMac, finIOS, sales string
	for _, l := range lines {
		f := strings.Fields(l)
		switch {
		case len(f) > 1 && f[0] == "Finance" && f[1] == "macOS":
			finMac = l
		case len(f) > 1 && f[0] == "Finance" && f[1] == "iOS":
			finIOS = l
		case len(f) > 1 && f[0] == "Sales":
			sales = l
		}
	}
	if f := strings.Fields(finMac); len(f) != 6 || f[2] != "max_version" || f[3] != "27.9.99" || f[4] != "7" || f[5] != "-" {
		t.Errorf("Finance macOS row: %q", finMac)
	}
	if f := strings.Fields(finIOS); len(f) != 6 || f[2] != "latest" || f[3] != "-" {
		t.Errorf("Finance iOS row should hide the maximum version in latest mode: %q", finIOS)
	}
	if !strings.Contains(sales, "major 30, minor 7") || strings.Contains(sales, "system") {
		t.Errorf("Sales deferrals: %q", sales)
	}
	if strings.Contains(r.out, "USER DEFERRALS") || strings.Contains(r.out, "KEEP UPDATED") {
		t.Errorf("the table should leave out USER DEFERRALS and KEEP UPDATED:\n%s", r.out)
	}
	if strings.Contains(r.out, "Laptops") || strings.Contains(r.out, "ENABLED") {
		t.Errorf("disabled settings should be left out:\n%s", r.out)
	}
	// Tree order: Acme's children (Finance, Sales) before Other Root.
	if strings.Index(r.out, "Finance") > strings.Index(r.out, "Sales") {
		t.Errorf("rows not in tree order:\n%s", r.out)
	}
	if !strings.Contains(r.out, "2 of 5 policies with enabled update settings (3 rows)") {
		t.Errorf("footer:\n%s", r.out)
	}
}

func TestPoliciesUpdatesFilters(t *testing.T) {
	r := runUpdates(t, Globals{}, &PoliciesUpdatesCmd{Policy: "Finance", OS: []string{"macOS"}, Disabled: true})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.out, "ENABLED") || !strings.Contains(r.out, "Laptops") || !strings.Contains(r.out, "13.9.9") {
		t.Errorf("--disabled should show Laptops' stored macOS settings:\n%s", r.out)
	}
	if strings.Contains(r.out, "iOS") || strings.Contains(r.out, "Sales") {
		t.Errorf("--os macos and --policy Finance should leave out iOS and Sales:\n%s", r.out)
	}
	if !strings.Contains(r.out, `2 of 2 policies in "Finance" and its sub-policies with stored update settings (2 rows)`) {
		t.Errorf("footer:\n%s", r.out)
	}

	r = runUpdates(t, Globals{}, &PoliciesUpdatesCmd{Policy: "Finance", Direct: true, Disabled: true})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Contains(r.out, "Laptops") {
		t.Errorf("--direct should leave out sub-policies:\n%s", r.out)
	}

	for _, cmd := range []*PoliciesUpdatesCmd{{OS: []string{"windows"}}, {Direct: true}} {
		if r := runUpdates(t, Globals{}, cmd); r.err == nil {
			t.Errorf("%+v should fail", cmd)
		}
	}
}

func TestPoliciesUpdatesCSVAndJSON(t *testing.T) {
	r := runUpdates(t, Globals{Output: "csv"}, &PoliciesUpdatesCmd{OS: []string{"macos"}})
	if r.err != nil {
		t.Fatal(r.err)
	}
	recs := parseCSV(t, r.out)
	head := strings.Join(recs[0][:5], ",")
	if head != "POLICY ID,POLICY,PATH,OS,ENABLED" || recs[0][len(recs[0])-1] != "LAST UPDATED" {
		t.Errorf("CSV header: %v", recs[0])
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}
	fin := recs[1]
	if fin[0] != "finance" || fin[2] != "Acme / Finance" || fin[col["ALLOWED DAYS"]] != "Mon Tue" ||
		fin[col["USER DEFERRALS"]] != "" || recs[2][col["USER DEFERRALS"]] != "3" || fin[col["LAST UPDATED"]] != "2026-10-01T10:00:00Z" {
		t.Errorf("Finance CSV row: %v", fin)
	}

	r = runUpdates(t, Globals{Output: "csv"}, &PoliciesUpdatesCmd{OS: []string{"ios"}})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if recs := parseCSV(t, r.out); recs[1][col["MAX VERSION"]] != "15.6" {
		t.Errorf("CSV should keep the stored maximum version in latest mode: %v", recs[1])
	}

	r = runUpdates(t, Globals{JSON: true}, &PoliciesUpdatesCmd{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["policy_id"] != "finance" || got[1]["policy_id"] != "sales" || got[0]["schedule"] == nil {
		t.Errorf("JSON should be the raw settings of Finance and Sales: %v", got)
	}
}

func TestPoliciesUpdatesWithoutPermission(t *testing.T) {
	ds := newDeviceServer(t)
	ds.updatesStatus = http.StatusForbidden
	r := runPoliciesOnServer(t, ds, policiesFixture, Globals{}, (&PoliciesUpdatesCmd{}).Run)
	if r.err == nil || !strings.Contains(r.err.Error(), `"View System Updates Settings"`) {
		t.Errorf("err = %v, want a hint about the permission", r.err)
	}
}
