package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	ds := newDeviceServer(t)
	g.APIKey, g.BaseURL = "k", ds.URL+"/api/v2"
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
	// Acme: 5 devices in it and its sub-policies, 2 children.
	acme := strings.Fields(lineContaining(t, out, "acme"))
	if len(acme) != 4 || acme[2] != "5" || acme[3] != "2" {
		t.Errorf("Acme row should be [id name devices=5 children=2], got %q", acme)
	}
	other := strings.Fields(lineContaining(t, out, "Other Root"))
	if len(other) != 5 || other[3] != "1" || other[4] != "0" { // name has two words
		t.Errorf("Other Root row should be [id Other Root devices=1 children=0], got %q", other)
	}
	if h := strings.Fields(lineContaining(t, out, "POLICY ID")); strings.Join(h, " ") != "POLICY ID NAME DEVICES CHILDREN" {
		t.Errorf("header = %q", h)
	}
	if !strings.Contains(out, "DEVICES counts the devices in a policy and all of its sub-policies") {
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

func TestPoliciesListCountsFetchOnlyTheLocationFact(t *testing.T) {
	r := runPoliciesWith(t, Globals{}, (&PoliciesListCmd{}).Run)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.ds.requests) == 0 {
		t.Fatal("expected device requests")
	}
	for _, req := range r.ds.requests {
		if len(req.facts) != 1 || req.facts[0] != "policy_id" {
			t.Errorf("only the policy_id fact should be requested, got %v", req.facts)
		}
	}
}

func TestPoliciesListCountsWarnWhenTotalsDisagree(t *testing.T) {
	ds := newDeviceServer(t)
	ds.totalOverride = 99
	var out, errOut bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: ds.URL + "/api/v2"}, Out: &out, Err: &errOut}
	if err := (&PoliciesListCmd{}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "Addigy reported 99 devices but 7 were received") {
		t.Errorf("expected a warning, got %q", errOut.String())
	}
}

func TestPoliciesListCSV(t *testing.T) {
	out := runPoliciesCSV(t, (&PoliciesListCmd{}).Run)
	want := "POLICY ID,NAME,DEVICES,CHILDREN,PARENT\n" +
		"acme,Acme,5,2,\n" +
		"other,Other Root,1,0,\n"
	if out != want {
		t.Errorf("CSV:\n%s\nwant:\n%s", out, want)
	}

	// The layout doesn't change with the scope; sub-policies name their parent.
	out = runPoliciesCSV(t, (&PoliciesListCmd{Parent: "finance"}).Run)
	want = "POLICY ID,NAME,DEVICES,CHILDREN,PARENT\nfin-laptops,Laptops,2,0,Finance\n"
	if out != want {
		t.Errorf("CSV:\n%s\nwant:\n%s", out, want)
	}

	out = runPoliciesCSV(t, (&PoliciesListCmd{NoCounts: true}).Run)
	if !strings.HasPrefix(out, "POLICY ID,NAME,CHILDREN,PARENT\n") {
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

func TestWithDeviceCountKeepsRawFieldsAndDoesNotEscapeHTML(t *testing.T) {
	got, err := withDeviceCount(json.RawMessage(`{"name":"<R&D>","n":1.50}`), 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"deviceCount":3,"n":1.50,"name":"<R&D>"}` {
		t.Errorf("got %s", got)
	}
	if _, err := withDeviceCount(json.RawMessage(`[1]`), 1); err == nil {
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

func TestPoliciesListRejectsAllWithParent(t *testing.T) {
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k"}, Out: io.Discard, Err: io.Discard}
	if err := (&PoliciesListCmd{All: true, Parent: "x"}).Run(app); err == nil {
		t.Error("expected an error for --all with --parent")
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
	if !strings.Contains(out, "[n] = devices in the policy and all of its sub-policies") {
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
