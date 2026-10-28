package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ginkio/addigyctl/internal/addigy"
)

// A device has exactly one location: its policy_id fact. It can also be
// auto-assigned to other policies; its policy_ids fact then lists those, the
// location and all their parents, as Addigy does.
type fixtureDevice struct {
	id, serial, location string
	autoAssigned         []string
}

// Policy hierarchy comes from policiesFixture:
//
//	acme ─┬─ finance ── fin-laptops
//	      └─ sales
//	other
var fixtureDevices = []fixtureDevice{
	{"d1", "B-2", "acme", nil},
	{"d2", "A-1", "finance", nil},
	{"d3", "C-3", "fin-laptops", nil},
	{"d4", "D-4", "fin-laptops", nil},
	{"d5", "E-5", "sales", nil},
	{"d6", "F-6", "", nil},                            // no location at all
	{"d9", "Z-9", "other", []string{"projects-2024"}}, // a policy only emptyPoliciesFixture has
}

type deviceRequest struct {
	page, perPage int
	facts         []string
	queryPolicy   string
	sortField     string
}

type deviceServer struct {
	*httptest.Server
	mu            sync.Mutex
	requests      []deviceRequest
	totalOverride int               // when set, reported in metadata.total instead of the real total
	policies      string            // when set, served instead of policiesFixture
	rules         string            // automatic assignment rules (JSON); default none
	rulesStatus   int               // when set, the rules endpoint answers this status instead
	extraFacts    map[string]string // agent ID → more facts, as raw JSON members
	updates       map[string]string // policy ID → system update settings (JSON); default all disabled
	updatesStatus int               // when set, the update settings endpoint answers this status instead
}

func newDeviceServer(t *testing.T) *deviceServer {
	t.Helper()
	ds := &deviceServer{}
	ds.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/oa/policies/query":
			if ds.policies != "" {
				io.WriteString(w, ds.policies)
			} else {
				io.WriteString(w, policiesFixture)
			}
		case "/api/v2/o/org1/policies/rules/":
			if ds.rulesStatus != 0 {
				w.WriteHeader(ds.rulesStatus)
				io.WriteString(w, `{"message":"Do not have permission to perform this action."}`)
				return
			}
			if ds.rules == "" {
				io.WriteString(w, `[]`)
				return
			}
			io.WriteString(w, ds.rules)
		case "/api/v2/system-updates/settings":
			if ds.updatesStatus != 0 {
				w.WriteHeader(ds.updatesStatus)
				io.WriteString(w, `{"message":"Do not have permission to perform this action."}`)
				return
			}
			id := r.URL.Query().Get("policy_id")
			if body, ok := ds.updates[id]; ok {
				io.WriteString(w, body)
				return
			}
			fmt.Fprintf(w, `{"policy_id":%q,"macos_settings":{"enabled":false},"ios_settings":{"enabled":false},"ipados_settings":{"enabled":false},"tvos_settings":{"enabled":false},"schedule":{"enabled":false},"last_updated":null}`, id)
		case "/api/v2/devices":
			var body struct {
				Page                   int      `json:"page"`
				PerPage                int      `json:"per_page"`
				SortField              string   `json:"sort_field"`
				DesiredFactIdentifiers []string `json:"desired_fact_identifiers"`
				Query                  struct {
					PolicyID string `json:"policy_id"`
				} `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("bad request body: %v", err)
			}
			ds.mu.Lock()
			ds.requests = append(ds.requests, deviceRequest{
				page: body.Page, perPage: body.PerPage, facts: body.DesiredFactIdentifiers,
				queryPolicy: body.Query.PolicyID, sortField: body.SortField,
			})
			ds.mu.Unlock()

			// The fake server always orders by serial number, like a real sort_field.
			list := append([]fixtureDevice(nil), fixtureDevices...)
			sort.Slice(list, func(i, j int) bool { return list[i].serial < list[j].serial })

			per, page := body.PerPage, body.Page
			if per <= 0 {
				per = 50
			}
			if page < 1 {
				page = 1
			}
			start := min((page-1)*per, len(list))
			end := min(start+per, len(list))

			parent := ds.parents()
			var items []string
			for _, d := range list[start:end] {
				facts := fmt.Sprintf(`"serial_number":{"type":"string","value":%q}`, d.serial)
				if d.location != "" {
					facts += fmt.Sprintf(`,"policy_id":{"type":"string","value":%q}`, d.location)
					ids, _ := json.Marshal(policyIDs(parent, append([]string{d.location}, d.autoAssigned...)))
					facts += fmt.Sprintf(`,"policy_ids":{"type":"list","value":%s}`, ids)
				}
				if extra := ds.extraFacts[d.id]; extra != "" {
					facts += "," + extra
				}
				items = append(items, fmt.Sprintf(`{"agentid":%q,"facts":{%s}}`, d.id, facts))
			}
			total := len(list)
			if ds.totalOverride != 0 {
				total = ds.totalOverride
			}
			fmt.Fprintf(w, `{"items":[%s],"metadata":{"page":%d,"page_count":%d,"per_page":%d,"result_count":%d,"total":%d}}`,
				strings.Join(items, ","), page, (len(list)+per-1)/per, per, end-start, total)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ds.Close)
	return ds
}

// parents maps each served policy to its parent.
func (ds *deviceServer) parents() map[string]string {
	src := ds.policies
	if src == "" {
		src = policiesFixture
	}
	var pols []struct {
		ID     string `json:"policyId"`
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal([]byte(src), &pols); err != nil {
		panic(err)
	}
	m := map[string]string{}
	for _, p := range pols {
		m[p.ID] = p.Parent
	}
	return m
}

// policyIDs returns the policies and all their parents, once each.
func policyIDs(parent map[string]string, policies []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range policies {
		for id := p; id != "" && !seen[id]; id = parent[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

type runResult struct {
	out, errOut string
	err         error
}

func (ds *deviceServer) run(t *testing.T, jsonOut bool, cmd *DevicesListCmd) runResult {
	t.Helper()
	var out, errOut bytes.Buffer
	app := &App{
		Ctx: context.Background(),
		G:   &Globals{APIKey: "k", BaseURL: ds.URL + "/api/v2", JSON: jsonOut},
		Out: &out,
		Err: &errOut,
	}
	err := cmd.Run(app)
	return runResult{out.String(), errOut.String(), err}
}

// order returns the position of each agent ID in out (failing if one is
// missing or listed twice).
func order(t *testing.T, out string, ids ...string) []int {
	t.Helper()
	pos := make([]int, len(ids))
	for i, id := range ids {
		pos[i] = strings.Index(out, id+" ")
		if pos[i] < 0 {
			t.Fatalf("%s missing from output:\n%s", id, out)
		}
		if strings.Count(out, id+" ") != 1 {
			t.Fatalf("%s listed more than once:\n%s", id, out)
		}
	}
	return pos
}

func assertOrdered(t *testing.T, out string, ids ...string) {
	t.Helper()
	p := order(t, out, ids...)
	for i := 1; i < len(p); i++ {
		if p[i-1] > p[i] {
			t.Errorf("expected order %v:\n%s", ids, out)
			return
		}
	}
}

func assertAbsent(t *testing.T, out string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if strings.Contains(out, id+" ") {
			t.Errorf("%s should not be listed:\n%s", id, out)
		}
	}
}

func TestDevicesListPolicyIncludesSubPolicies(t *testing.T) {
	ds := newDeviceServer(t)
	r := ds.run(t, false, &DevicesListCmd{Policy: "Acme", Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}

	// Located in acme, finance, fin-laptops or sales, ordered by serial number.
	assertOrdered(t, r.out, "d2", "d1", "d3", "d4", "d5")
	assertAbsent(t, r.out, "d6", "d9") // no location / unrelated policy
	for _, want := range []string{
		`5 of 5 devices in "Acme" and 3 sub-policies`,
		"LOCATION",
		"Acme / Finance / Laptops",
		"Acme / Sales",
	} {
		if !strings.Contains(r.out, want) {
			t.Errorf("output missing %q:\n%s", want, r.out)
		}
	}

	// The server can't filter on a subtree, so no policy filter is sent, but
	// the location fact must be requested.
	for _, req := range ds.requests {
		if req.queryPolicy != "" {
			t.Errorf("unexpected server-side policy filter %q", req.queryPolicy)
		}
		if !contains(req.facts, "policy_id") || !contains(req.facts, "serial_number") {
			t.Errorf("request must ask for the location and sort facts, got %v", req.facts)
		}
	}
}

func TestDevicesListPolicyFetchesAllPages(t *testing.T) {
	defer func(old int) { fetchPageSize = old }(fetchPageSize)
	fetchPageSize = 2 // 7 devices -> 4 pages

	ds := newDeviceServer(t)
	r := ds.run(t, false, &DevicesListCmd{Policy: "acme", Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}
	assertOrdered(t, r.out, "d2", "d1", "d3", "d4", "d5")
	if r.errOut != "" {
		t.Errorf("unexpected warning: %s", r.errOut)
	}

	var pages []int
	for _, req := range ds.requests {
		pages = append(pages, req.page)
	}
	sort.Ints(pages)
	if fmt.Sprint(pages) != "[1 2 3 4]" {
		t.Errorf("each page should be requested exactly once, got %v", pages)
	}
}

func TestDevicesListDirectOnlyListsTheLocationItself(t *testing.T) {
	ds := newDeviceServer(t)
	r := ds.run(t, false, &DevicesListCmd{Policy: "Acme", Direct: true, Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}
	order(t, r.out, "d1")
	assertAbsent(t, r.out, "d2", "d3", "d4", "d5", "d6", "d9")
	if !strings.Contains(r.out, `1 of 1 devices in "Acme"`) {
		t.Errorf("unexpected footer:\n%s", r.out)
	}
	if strings.Contains(r.out, "sub-polic") || strings.Contains(r.out, "LOCATION") {
		t.Errorf("--direct output should not mention sub-policies or add a LOCATION column:\n%s", r.out)
	}
}

func TestDevicesListPolicyByPathAndNameOfSubPolicy(t *testing.T) {
	ds := newDeviceServer(t)
	for _, ref := range []string{"acme/finance", "Acme / Finance", "finance"} {
		r := ds.run(t, false, &DevicesListCmd{Policy: ref, Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
		if r.err != nil {
			t.Fatalf("%q: %v", ref, r.err)
		}
		assertOrdered(t, r.out, "d2", "d3", "d4")
		assertAbsent(t, r.out, "d1", "d5")
		if want := `3 of 3 devices in "Finance" and 1 sub-policy`; !strings.Contains(r.out, want) {
			t.Errorf("%q: footer missing %q:\n%s", ref, want, r.out)
		}
	}
}

func TestDevicesListPolicyPaginatesClientSide(t *testing.T) {
	ds := newDeviceServer(t)
	r := ds.run(t, true, &DevicesListCmd{Policy: "acme", Facts: []string{"serial_number"}, Page: 2, PerPage: 2})
	if r.err != nil {
		t.Fatal(r.err)
	}
	var got struct {
		Items []struct {
			AgentID string `json:"agentid"`
		} `json:"items"`
		Metadata struct {
			Page        int `json:"page"`
			PageCount   int `json:"page_count"`
			PerPage     int `json:"per_page"`
			ResultCount int `json:"result_count"`
			Total       int `json:"total"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, r.out)
	}
	// Sorted: d2, d1, d3, d4, d5 -> page 2 (2 per page) = d3, d4.
	if len(got.Items) != 2 || got.Items[0].AgentID != "d3" || got.Items[1].AgentID != "d4" {
		t.Errorf("page 2 should hold d3,d4: %s", r.out)
	}
	m := got.Metadata
	if m.Page != 2 || m.PageCount != 3 || m.PerPage != 2 || m.ResultCount != 2 || m.Total != 5 {
		t.Errorf("metadata = %+v", m)
	}
}

func TestDevicesListPolicySortDescending(t *testing.T) {
	ds := newDeviceServer(t)
	r := ds.run(t, false, &DevicesListCmd{Policy: "acme", Sort: "serial_number", Desc: true, Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}
	assertOrdered(t, r.out, "d5", "d4", "d3", "d1", "d2")
}

func TestDevicesListPolicyWarnsWhenTotalsDisagree(t *testing.T) {
	ds := newDeviceServer(t)
	ds.totalOverride = 99
	r := ds.run(t, false, &DevicesListCmd{Policy: "acme", Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.errOut, "reported 99 devices but 7 were received") || !strings.Contains(r.errOut, "incomplete") {
		t.Errorf("expected an incompleteness warning, got %q", r.errOut)
	}
}

func TestDevicesListWithoutPolicyStaysServerSide(t *testing.T) {
	ds := newDeviceServer(t)
	r := ds.run(t, false, &DevicesListCmd{Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}
	assertOrdered(t, r.out, "d2", "d1", "d3", "d4", "d5", "d6", "d9")
	if !strings.Contains(r.out, "7 of 7 devices\n") || strings.Contains(r.out, "LOCATION") {
		t.Errorf("unexpected output:\n%s", r.out)
	}
	if len(ds.requests) != 1 || contains(ds.requests[0].facts, "policy_id") {
		t.Errorf("without --policy only the requested facts should be fetched: %+v", ds.requests)
	}
}

func TestDevicesListPolicyErrors(t *testing.T) {
	ds := newDeviceServer(t)
	if r := ds.run(t, false, &DevicesListCmd{Direct: true, Page: 1, PerPage: 50}); r.err == nil {
		t.Error("--direct without --policy should fail")
	}
	if r := ds.run(t, false, &DevicesListCmd{Policy: "no such policy", Page: 1, PerPage: 50}); r.err == nil {
		t.Error("an unknown policy should fail")
	}
	if len(ds.requests) != 0 {
		t.Errorf("no device search should run for invalid input, got %d", len(ds.requests))
	}
}

func TestSortDevices(t *testing.T) {
	dev := func(id string, v any) addigy.Device {
		d := addigy.Device{AgentID: id, Facts: map[string]addigy.Fact{}}
		if v != nil {
			d.Facts["n"] = addigy.Fact{Value: v}
		}
		return d
	}
	ids := func(ds []addigy.Device) string {
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = d.AgentID
		}
		return strings.Join(out, ",")
	}

	ds := []addigy.Device{dev("m", nil), dev("a", float64(10)), dev("b", float64(9)), dev("c", float64(100))}
	sortDevices(ds, "n", false)
	if got := ids(ds); got != "b,a,c,m" {
		t.Errorf("ascending (numeric, missing last) = %s", got)
	}
	sortDevices(ds, "n", true)
	if got := ids(ds); got != "c,a,b,m" {
		t.Errorf("descending (missing still last) = %s", got)
	}

	if compareValues("abc", "ABD") >= 0 {
		t.Error("text should compare case-insensitively")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestDevicesListNoPolicy(t *testing.T) {
	ds := newDeviceServer(t)
	r := ds.run(t, false, &DevicesListCmd{NoPolicy: true, Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}
	order(t, r.out, "d6")
	assertAbsent(t, r.out, "d1", "d2", "d3", "d4", "d5", "d9")
	if !strings.Contains(r.out, "1 of 1 devices without any policy") {
		t.Errorf("footer missing:\n%s", r.out)
	}
	for _, req := range ds.requests {
		for _, f := range []string{"serial_number", addigy.LocationFact, policyIDsFact} {
			if !contains(req.facts, f) {
				t.Errorf("request lacks fact %s: %v", f, req.facts)
			}
		}
	}

	if r := ds.run(t, false, &DevicesListCmd{NoPolicy: true, Policy: "acme", Page: 1, PerPage: 50}); r.err == nil {
		t.Error("--no-policy with --policy should fail")
	}
}

func TestHasPolicy(t *testing.T) {
	dev := func(facts map[string]any) addigy.Device {
		d := addigy.Device{Facts: map[string]addigy.Fact{}}
		for k, v := range facts {
			d.Facts[k] = addigy.Fact{Value: v}
		}
		return d
	}
	for name, tc := range map[string]struct {
		facts map[string]any
		want  bool
	}{
		"no facts":         {nil, false},
		"empty values":     {map[string]any{"policy_id": "", "policy_ids": []any{}}, false},
		"null values":      {map[string]any{"policy_id": nil, "policy_ids": nil}, false},
		"location":         {map[string]any{"policy_id": "acme"}, true},
		"applied policies": {map[string]any{"policy_ids": []any{"acme"}}, true},
	} {
		if got := hasPolicy(dev(tc.facts)); got != tc.want {
			t.Errorf("%s: hasPolicy = %v, want %v", name, got, tc.want)
		}
	}
}

// mdmFixture: d1 has no MDM, d2 MDM without an approved profile, d3 both,
// and d4 reports has_mdm only (like an iPhone); the rest report neither.
var mdmFixture = map[string]string{
	"d1": `"has_mdm":{"type":"boolean","value":false},"has_mdm_profile_approved":{"type":"boolean","value":false}`,
	"d2": `"has_mdm":{"type":"boolean","value":true},"has_mdm_profile_approved":{"type":"boolean","value":false}`,
	"d3": `"has_mdm":{"type":"boolean","value":true},"has_mdm_profile_approved":{"type":"boolean","value":true}`,
	"d4": `"has_mdm":{"type":"boolean","value":true}`,
}

func TestDevicesListMDMIssues(t *testing.T) {
	ds := newDeviceServer(t)
	ds.extraFacts = mdmFixture
	r := ds.run(t, false, &DevicesListCmd{MDMIssues: true, Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}
	assertOrdered(t, r.out, "d2", "d1") // by serial number: A-1, B-2
	assertAbsent(t, r.out, "d3", "d4", "d5", "d6", "d9")
	for _, want := range []string{"HAS MDM", "HAS MDM PROFILE APPROVED", "2 of 2 devices with MDM issues"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("missing %q:\n%s", want, r.out)
		}
	}

	// Combined with --policy, both filters apply.
	ds = newDeviceServer(t)
	ds.extraFacts = mdmFixture
	r = ds.run(t, false, &DevicesListCmd{MDMIssues: true, Policy: "finance", Facts: []string{"serial_number"}, Page: 1, PerPage: 50})
	if r.err != nil {
		t.Fatal(r.err)
	}
	order(t, r.out, "d2")
	assertAbsent(t, r.out, "d1", "d3", "d4")
	if !strings.Contains(r.out, `1 of 1 devices in "Finance" and 1 sub-policy with MDM issues`) {
		t.Errorf("footer:\n%s", r.out)
	}
}
