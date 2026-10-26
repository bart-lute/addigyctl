package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const policiesFixture = `[
  {"policyId":"acme","name":"Acme","parent":null,"last_deployed":"2026-09-01"},
  {"policyId":"finance","name":"Finance","parent":"acme"},
  {"policyId":"sales","name":"Sales","parent":"acme"},
  {"policyId":"fin-laptops","name":"Laptops","parent":"finance"},
  {"policyId":"other","name":"Other Root","parent":null}
]`

// policiesRun is the result of running a policies command against the fake
// server (which also serves fixtureDevices).
type policiesRun struct {
	out, errOut string
	err         error
	ds          *deviceServer
}

func runPoliciesWith(t *testing.T, g Globals, run func(app *App) error) policiesRun {
	t.Helper()
	return runPoliciesOn(t, "", g, run)
}

// runPoliciesOn is runPoliciesWith with another policy list (JSON) than
// policiesFixture, if set.
func runPoliciesOn(t *testing.T, policies string, g Globals, run func(app *App) error) policiesRun {
	t.Helper()
	return runPoliciesOnServer(t, newDeviceServer(t), policies, g, run)
}

func runPoliciesOnServer(t *testing.T, ds *deviceServer, policies string, g Globals, run func(app *App) error) policiesRun {
	t.Helper()
	ds.policies = policies
	g.APIKey, g.BaseURL = "k", ds.URL+"/api/v2"
	if g.OrgID == "" {
		g.OrgID = "org1"
	}
	var out, errOut bytes.Buffer
	app := &App{Ctx: context.Background(), G: &g, Out: &out, Err: &errOut}
	err := run(app)
	return policiesRun{out.String(), errOut.String(), err, ds}
}

func runPolicies(t *testing.T, jsonOut bool, run func(app *App) error) string {
	t.Helper()
	r := runPoliciesWith(t, Globals{JSON: jsonOut}, run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	return r.out
}

func runPoliciesCSV(t *testing.T, run func(app *App) error) string {
	t.Helper()
	r := runPoliciesWith(t, Globals{Output: "csv"}, run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	return r.out
}

func TestPoliciesListDefaultShowsRootsOnly(t *testing.T) {
	out := runPolicies(t, false, (&PoliciesListCmd{}).Run)
	for _, want := range []string{"Acme", "Other Root", "2 root policies", "--parent"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Finance", "Sales", "Laptops", "PARENT", "LAST DEPLOYED"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("root view should not contain %q:\n%s", unwanted, out)
		}
	}
	// Acme: 5 devices in it and its sub-policies, no auto-assignment, 2 children.
	acme := strings.Fields(lineContaining(t, out, "acme"))
	if len(acme) != 5 || acme[2] != "5" || acme[3] != "-" || acme[4] != "2" {
		t.Errorf("Acme row should be [id name devices=5 auto-assign=- children=2], got %q", acme)
	}
	other := strings.Fields(lineContaining(t, out, "Other Root"))
	if len(other) != 6 || other[3] != "1" || other[5] != "0" { // name has two words
		t.Errorf("Other Root row should be [id Other Root devices=1 auto-assign=- children=0], got %q", other)
	}
	if h := strings.Fields(lineContaining(t, out, "POLICY ID")); strings.Join(h, " ") != "POLICY ID NAME DEVICES AUTO-ASSIGN CHILDREN" {
		t.Errorf("header = %q", h)
	}
	if !strings.Contains(out, "DEVICES counts the devices a policy applies to, in it or its sub-policies, auto-assigned ones included") {
		t.Errorf("the DEVICES column should be explained:\n%s", out)
	}
}

func TestPoliciesListDevicesIncludeSubPolicies(t *testing.T) {
	out := runPolicies(t, false, (&PoliciesListCmd{All: true}).Run)
	for id, want := range map[string]string{"acme": "5", "finance": "3", "sales": "1", "fin-laptops": "2"} {
		f := strings.Fields(lineContaining(t, out, id+" "))
		// [id name devices children parent...]
		if f[2] != want {
			t.Errorf("%s: DEVICES = %s, want %s\n%s", id, f[2], want, out)
		}
	}
}

// TestPoliciesListSortDevicesDefaultsToMostFirst checks that --sort devices
// defaults to busiest-first, not alphabetical-count-ascending: devices is a
// count, where more is usually more interesting, unlike name/id/parent.
func TestPoliciesListSortDevicesDefaultsToMostFirst(t *testing.T) {
	// acme=5, finance=3, fin-laptops=2 devices (including sub-policies).
	out := runPolicies(t, false, (&PoliciesListCmd{All: true, Sort: "devices"}).Run)
	assertOrdered(t, out, "acme", "finance", "fin-laptops")
}

// TestPoliciesListSortDevicesDescReversesToFewestFirst checks that --desc
// reverses devices' own default (most first) to fewest first, rather than
// always meaning "descending" on top of an ascending default.
func TestPoliciesListSortDevicesDescReversesToFewestFirst(t *testing.T) {
	out := runPolicies(t, false, (&PoliciesListCmd{All: true, Sort: "devices", Desc: true}).Run)
	assertOrdered(t, out, "fin-laptops", "finance", "acme")
}

func TestPoliciesListNoCountsSkipsTheDeviceFetch(t *testing.T) {
	r := runPoliciesWith(t, Globals{}, (&PoliciesListCmd{NoCounts: true}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Contains(r.out, "DEVICES") {
		t.Errorf("--no-counts should drop the column:\n%s", r.out)
	}
	if len(r.ds.requests) != 0 {
		t.Errorf("no device requests expected, got %d", len(r.ds.requests))
	}
}

func TestPoliciesListCountsFetchOnlyThePolicyIDsFact(t *testing.T) {
	r := runPoliciesWith(t, Globals{}, (&PoliciesListCmd{}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.ds.requests) == 0 {
		t.Fatal("expected device requests")
	}
	for _, req := range r.ds.requests {
		if len(req.facts) != 1 || req.facts[0] != "policy_ids" {
			t.Errorf("only the policy_ids fact should be requested, got %v", req.facts)
		}
	}
}

func TestPoliciesListCountsWarnWhenTotalsDisagree(t *testing.T) {
	ds := newDeviceServer(t)
	ds.totalOverride = 99
	var out, errOut bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: ds.URL + "/api/v2", OrgID: "org1"}, Out: &out, Err: &errOut}
	if err := (&PoliciesListCmd{}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "Addigy reported 99 devices but 7 were received") {
		t.Errorf("expected a warning, got %q", errOut.String())
	}
}

func TestPoliciesListCSV(t *testing.T) {
	out := runPoliciesCSV(t, (&PoliciesListCmd{}).Run)
	want := "POLICY ID,NAME,DEVICES,AUTO-ASSIGN,CHILDREN,PARENT\n" +
		"acme,Acme,5,,2,\n" +
		"other,Other Root,1,,0,\n"
	if out != want {
		t.Errorf("CSV:\n%s\nwant:\n%s", out, want)
	}

	// The layout doesn't change with the scope; sub-policies name their parent.
	out = runPoliciesCSV(t, (&PoliciesListCmd{Parent: "finance"}).Run)
	want = "POLICY ID,NAME,DEVICES,AUTO-ASSIGN,CHILDREN,PARENT\nfin-laptops,Laptops,2,,0,Finance\n"
	if out != want {
		t.Errorf("CSV:\n%s\nwant:\n%s", out, want)
	}

	out = runPoliciesCSV(t, (&PoliciesListCmd{NoCounts: true}).Run)
	if !strings.HasPrefix(out, "POLICY ID,NAME,AUTO-ASSIGN,CHILDREN,PARENT\n") {
		t.Errorf("--no-counts CSV:\n%s", out)
	}
}

func TestPoliciesListJSONAddsDeviceCount(t *testing.T) {
	out := runPolicies(t, true, (&PoliciesListCmd{All: true}).Run)
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	byID := map[string]map[string]any{}
	for _, p := range got {
		byID[p["policyId"].(string)] = p
	}
	if byID["acme"]["deviceCount"] != 5.0 || byID["finance"]["deviceCount"] != 3.0 {
		t.Errorf("deviceCount missing or wrong: %s", out)
	}
	if byID["acme"]["last_deployed"] != "2026-09-01" {
		t.Errorf("the other fields must be kept: %s", out)
	}

	r := runPoliciesWith(t, Globals{JSON: true}, (&PoliciesListCmd{NoCounts: true}).Run)
	if r.err != nil || strings.Contains(r.out, "deviceCount") {
		t.Errorf("--no-counts JSON should be the raw policies: %v\n%s", r.err, r.out)
	}
}

func TestWithFieldsKeepsRawFieldsAndDoesNotEscapeHTML(t *testing.T) {
	got, err := withFields(json.RawMessage(`{"name":"<R&D>","n":1.50}`), map[string]any{"deviceCount": 3, "autoAssign": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"autoAssign":"true","deviceCount":3,"n":1.50,"name":"<R&D>"}` {
		t.Errorf("got %s", got)
	}
	if _, err := withFields(json.RawMessage(`[1]`), map[string]any{"deviceCount": 1}); err == nil {
		t.Error("a non-object should be an error")
	}
}

func TestPoliciesListParentByName(t *testing.T) {
	out := runPolicies(t, false, (&PoliciesListCmd{Parent: "acme"}).Run)
	for _, want := range []string{"Finance", "Sales", `2 sub-policies of "Acme"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Laptops") || strings.Contains(out, "Other Root") {
		t.Errorf("only direct children expected:\n%s", out)
	}
}

func TestPoliciesListAllAndNameSearchSpanLevels(t *testing.T) {
	out := runPolicies(t, false, (&PoliciesListCmd{All: true}).Run)
	if !strings.Contains(out, "5 policies") || !strings.Contains(out, "PARENT") {
		t.Errorf("--all should list every level with a PARENT column:\n%s", out)
	}

	out = runPolicies(t, false, (&PoliciesListCmd{Name: "lap"}).Run)
	if !strings.Contains(out, "Laptops") || !strings.Contains(out, "Finance") || !strings.Contains(out, "1 policy") {
		t.Errorf("--name should search sub-policies and show their parent:\n%s", out)
	}
}

func TestPoliciesListJSONKeepsRawItems(t *testing.T) {
	out := runPolicies(t, true, (&PoliciesListCmd{}).Run)
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(got) != 2 || got[0]["policyId"] != "acme" {
		t.Errorf("unexpected JSON: %s", out)
	}
}

// emptyPoliciesFixture is policiesFixture plus policies no device in
// fixtureDevices is located in; d9 (located in other) is auto-assigned to
// projects-2024:
//
//	acme ─┬─ finance ── fin-laptops
//	      ├─ sales ── sales-old (empty)
//	      └─ projects ── projects-2024 (d9, auto-assigned)
//	other (d9)
//	archive (empty)
const emptyPoliciesFixture = `[
  {"policyId":"acme","name":"Acme","parent":null},
  {"policyId":"finance","name":"Finance","parent":"acme"},
  {"policyId":"sales","name":"Sales","parent":"acme"},
  {"policyId":"fin-laptops","name":"Laptops","parent":"finance"},
  {"policyId":"other","name":"Other Root","parent":null},
  {"policyId":"sales-old","name":"Old Sales","parent":"sales"},
  {"policyId":"projects","name":"Projects","parent":"acme"},
  {"policyId":"projects-2024","name":"Projects 2024","parent":"projects"},
  {"policyId":"archive","name":"Archive","parent":null}
]`

func TestPoliciesListAllBelowParent(t *testing.T) {
	r := runPoliciesOn(t, emptyPoliciesFixture, Globals{}, (&PoliciesListCmd{All: true, Parent: "Acme"}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	// Every level below Acme, with the parent column; not Acme itself, nor
	// anything outside it.
	for _, want := range []string{"Finance", "Laptops", "Sales", "Old Sales", "Projects 2024", "PARENT", `6 policies below "Acme"`} {
		if !strings.Contains(r.out, want) {
			t.Errorf("output missing %q:\n%s", want, r.out)
		}
	}
	for _, unwanted := range []string{"acme ", "Other Root", "Archive"} {
		if strings.Contains(r.out, unwanted) {
			t.Errorf("output should not contain %q:\n%s", unwanted, r.out)
		}
	}
}

func TestPoliciesListEmpty(t *testing.T) {
	ids := func(out string) []string {
		var got []string
		for _, id := range []string{"acme", "finance", "fin-laptops", "sales", "sales-old", "projects", "projects-2024", "other", "archive"} {
			for _, line := range strings.Split(out, "\n") {
				if f := strings.Fields(line); len(f) > 0 && f[0] == id {
					got = append(got, id)
				}
			}
		}
		return got
	}

	// Counted like Addigy's web app: an auto-assigned device counts for the
	// policy and its parents, besides counting where it is located.
	all := runPoliciesOn(t, emptyPoliciesFixture, Globals{}, (&PoliciesListCmd{All: true}).Run)
	if all.err != nil {
		t.Fatal(all.err)
	}
	for id, want := range map[string]string{"acme": "6", "projects": "1", "projects-2024": "1", "other": "1", "sales": "1", "sales-old": "0"} {
		if f := strings.Fields(lineContaining(t, all.out, id+" ")); f[len(f)-4] != want {
			t.Errorf("DEVICES of %s = %s, want %s (row %q)", id, f[len(f)-4], want, f)
		}
	}

	// Every level: only what applies to no device. A parent whose devices
	// sit in its sub-policies (finance) is not empty, nor is a policy with
	// only an auto-assigned device (projects-2024) or its parent (projects).
	r := runPoliciesOn(t, emptyPoliciesFixture, Globals{}, (&PoliciesListCmd{Empty: true}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := strings.Join(ids(r.out), ","); got != "sales-old,archive" {
		t.Errorf("--empty listed %s:\n%s", got, r.out)
	}
	if !strings.Contains(r.out, "2 policies without devices") || !strings.Contains(r.out, "PARENT") {
		t.Errorf("footer or PARENT column missing:\n%s", r.out)
	}

	// Below one policy, at every level.
	r = runPoliciesOn(t, emptyPoliciesFixture, Globals{}, (&PoliciesListCmd{Empty: true, All: true, Parent: "Acme"}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := strings.Join(ids(r.out), ","); got != "sales-old" {
		t.Errorf("--empty --all --parent Acme listed %s:\n%s", got, r.out)
	}
	if !strings.Contains(r.out, `1 policy below "Acme" without devices`) {
		t.Errorf("footer:\n%s", r.out)
	}

	// Direct sub-policies only.
	r = runPoliciesOn(t, emptyPoliciesFixture, Globals{}, (&PoliciesListCmd{Empty: true, Parent: "Acme"}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := ids(r.out); len(got) != 0 {
		t.Errorf("--empty --parent Acme listed %v:\n%s", got, r.out)
	}
	r = runPoliciesOn(t, emptyPoliciesFixture, Globals{}, (&PoliciesListCmd{Empty: true, Parent: "Sales"}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := strings.Join(ids(r.out), ","); got != "sales-old" || !strings.Contains(r.out, `1 sub-policy of "Sales" without devices`) {
		t.Errorf("--empty --parent Sales listed %s:\n%s", got, r.out)
	}

	// CSV for scripts: the same rows.
	r = runPoliciesOn(t, emptyPoliciesFixture, Globals{Output: "csv"}, (&PoliciesListCmd{Empty: true}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if recs := parseCSV(t, r.out); len(recs) != 3 || recs[1][0] != "archive" {
		t.Errorf("csv = %q", recs)
	}
}

func TestPoliciesListEmptyNeedsCounts(t *testing.T) {
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k"}, Out: io.Discard, Err: io.Discard}
	if err := (&PoliciesListCmd{Empty: true, NoCounts: true}).Run(app); err == nil || !strings.Contains(err.Error(), "--no-counts") {
		t.Errorf("err = %v, want a refusal of --empty with --no-counts", err)
	}
}

func TestPoliciesTreeCommand(t *testing.T) {
	out := runPolicies(t, false, (&PoliciesTreeCmd{NoCounts: true}).Run)
	for _, want := range []string{"Acme\n├── Finance\n│   └── Laptops\n└── Sales\nOther Root\n", "5 policies"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree output missing %q:\n%s", want, out)
		}
	}

	out = runPolicies(t, false, (&PoliciesTreeCmd{Ref: "Finance", IDs: true, NoCounts: true}).Run)
	if !strings.Contains(out, "Finance (finance)\n└── Laptops (fin-laptops)\n") {
		t.Errorf("subtree output:\n%s", out)
	}
}

func TestPoliciesTreeShowsDeviceCounts(t *testing.T) {
	out := runPolicies(t, false, (&PoliciesTreeCmd{}).Run)
	want := "Acme [5]\n├── Finance [3]\n│   └── Laptops [2]\n└── Sales [1]\nOther Root [1]\n"
	if !strings.Contains(out, want) {
		t.Errorf("tree with counts:\n%s\nwant:\n%s", out, want)
	}
	if !strings.Contains(out, "[n] = devices the policy applies to, in it or its sub-policies, auto-assigned ones included") {
		t.Errorf("the [n] notation should be explained:\n%s", out)
	}

	out = runPolicies(t, false, (&PoliciesTreeCmd{Ref: "Finance", IDs: true}).Run)
	if !strings.Contains(out, "Finance (finance) [3]\n└── Laptops (fin-laptops) [2]\n") {
		t.Errorf("subtree with IDs and counts:\n%s", out)
	}

	// --depth hides levels but the totals still include them.
	out = runPolicies(t, false, (&PoliciesTreeCmd{Depth: 1}).Run)
	if !strings.Contains(out, "Acme [5]\n├── Finance [3]\n└── Sales [1]\n") || strings.Contains(out, "Laptops") {
		t.Errorf("depth-limited tree:\n%s", out)
	}
}

func TestPoliciesTreeJSONHasDeviceCounts(t *testing.T) {
	out := runPolicies(t, true, (&PoliciesTreeCmd{Ref: "finance"}).Run)
	want := `[
  {
    "policyId": "finance",
    "name": "Finance",
    "deviceCount": 3,
    "children": [
      {
        "policyId": "fin-laptops",
        "name": "Laptops",
        "deviceCount": 2
      }
    ]
  }
]
`
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestPoliciesTreeCSVIsFlat(t *testing.T) {
	out := runPoliciesCSV(t, (&PoliciesTreeCmd{}).Run)
	want := "POLICY ID,NAME,PATH,DEPTH,DEVICES\n" +
		"acme,Acme,Acme,0,5\n" +
		"finance,Finance,Acme / Finance,1,3\n" +
		"fin-laptops,Laptops,Acme / Finance / Laptops,2,2\n" +
		"sales,Sales,Acme / Sales,1,1\n" +
		"other,Other Root,Other Root,0,1\n"
	if out != want {
		t.Errorf("CSV:\n%s\nwant:\n%s", out, want)
	}

	// Starting below the root: the path stays complete, depth is relative.
	r := runPoliciesWith(t, Globals{Output: "csv"}, (&PoliciesTreeCmd{Ref: "finance", NoCounts: true}).Run)
	want = "POLICY ID,NAME,PATH,DEPTH\nfinance,Finance,Acme / Finance,0\nfin-laptops,Laptops,Acme / Finance / Laptops,1\n"
	if r.err != nil || r.out != want {
		t.Errorf("CSV: %v\n%s\nwant:\n%s", r.err, r.out, want)
	}
}

func TestJSONOnlyCommandsRejectCSV(t *testing.T) {
	r := runPoliciesWith(t, Globals{Output: "csv"}, (&PoliciesGetCmd{Ref: "acme"}).Run)
	if r.err == nil || !strings.Contains(r.err.Error(), "policies get has no CSV output") {
		t.Errorf("policies get: %v", r.err)
	}
	r = runPoliciesWith(t, Globals{Output: "csv"}, (&ConfigShowCmd{}).Run)
	if r.err == nil || !strings.Contains(r.err.Error(), "config show has no CSV output") {
		t.Errorf("config show: %v", r.err)
	}
}

func TestPoliciesGetByNameAndID(t *testing.T) {
	for _, ref := range []string{"Finance", "finance"} {
		out := runPolicies(t, false, (&PoliciesGetCmd{Ref: ref}).Run)
		if !strings.Contains(out, `"policyId": "finance"`) {
			t.Errorf("get %q:\n%s", ref, out)
		}
	}
}

func TestPoliciesListBorders(t *testing.T) {
	plain := runPolicies(t, false, (&PoliciesListCmd{}).Run)
	if strings.Contains(plain, "┌") {
		t.Errorf("borders should be off by default:\n%s", plain)
	}

	on := true
	r := runPoliciesWith(t, Globals{Borders: &on}, (&PoliciesListCmd{}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.out, "┌") || !strings.Contains(r.out, "│ NAME") {
		t.Errorf("--borders should draw a bordered table:\n%s", r.out)
	}

	off := false
	r = runPoliciesWith(t, Globals{Borders: &off}, func(app *App) error {
		app.Cfg.Borders = true // --no-borders must override the config default
		return (&PoliciesListCmd{}).Run(app)
	})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Contains(r.out, "┌") {
		t.Errorf("--no-borders should override a config default of true:\n%s", r.out)
	}
}

func lineContaining(t *testing.T, s, sub string) string {
	t.Helper()
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	t.Fatalf("no line containing %q in:\n%s", sub, s)
	return ""
}

// autoAssignRulesFixture: projects-2024 has an enabled rule (plus the
// unassign rule that undoes it), sales-old only a disabled one, and archive
// only an unassign rule, which assigns nothing.
const autoAssignRulesFixture = `[
  {"rule_id":"r1","disabled":false,"tags":[{"key":"policy_id","value":"projects-2024"},{"key":"type","value":"auto-assignment"},{"key":"action","value":"assign"}]},
  {"rule_id":"r2","disabled":false,"tags":[{"key":"policy_id","value":"projects-2024"},{"key":"type","value":"auto-assignment"},{"key":"action","value":"unassign"},{"key":"rule_id","value":"r1"}]},
  {"rule_id":"r3","disabled":true,"tags":[{"key":"policy_id","value":"sales-old"},{"key":"type","value":"auto-assignment"},{"key":"action","value":"assign"}]},
  {"rule_id":"r4","disabled":false,"tags":[{"key":"policy_id","value":"archive"},{"key":"type","value":"auto-assignment"},{"key":"action","value":"unassign"}]}
]`

func TestPoliciesListAutoAssign(t *testing.T) {
	ds := newDeviceServer(t)
	ds.rules = autoAssignRulesFixture
	r := runPoliciesOnServer(t, ds, emptyPoliciesFixture, Globals{Output: "csv"}, (&PoliciesListCmd{All: true}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := map[string]string{}
	for _, rec := range parseCSV(t, r.out)[1:] {
		got[rec[0]] = rec[3]
	}
	for id, want := range map[string]string{"projects-2024": "true", "sales-old": "disabled", "archive": "", "acme": ""} {
		if got[id] != want {
			t.Errorf("AUTO-ASSIGN of %s = %q, want %q", id, got[id], want)
		}
	}

	ds = newDeviceServer(t)
	ds.rules = autoAssignRulesFixture
	r = runPoliciesOnServer(t, ds, emptyPoliciesFixture, Globals{JSON: true}, (&PoliciesListCmd{All: true}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	var pols []map[string]any
	if err := json.Unmarshal([]byte(r.out), &pols); err != nil {
		t.Fatal(err)
	}
	for _, p := range pols {
		v, has := p["autoAssign"]
		switch p["policyId"] {
		case "projects-2024":
			if v != "true" {
				t.Errorf("JSON autoAssign of projects-2024 = %v", v)
			}
		case "sales-old":
			if v != "disabled" {
				t.Errorf("JSON autoAssign of sales-old = %v", v)
			}
		default:
			if has {
				t.Errorf("%v has autoAssign %v, want none", p["policyId"], v)
			}
		}
	}
}

func TestPoliciesListWithoutAutoAssignPermission(t *testing.T) {
	ds := newDeviceServer(t)
	ds.rulesStatus = http.StatusForbidden
	r := runPoliciesOnServer(t, ds, "", Globals{}, (&PoliciesListCmd{}).Run)
	if r.err != nil {
		t.Fatalf("policies list should still work: %v", r.err)
	}
	if strings.Contains(r.out, "AUTO-ASSIGN") {
		t.Errorf("the column should be left out:\n%s", r.out)
	}
	if !strings.Contains(r.errOut, `"Automatic Policy Assignments"`) {
		t.Errorf("no note about the permission: %q", r.errOut)
	}

	ds = newDeviceServer(t)
	ds.rulesStatus = http.StatusInternalServerError
	if r := runPoliciesOnServer(t, ds, "", Globals{}, (&PoliciesListCmd{}).Run); r.err == nil {
		t.Error("another error fetching the rules should fail the command")
	}
}
