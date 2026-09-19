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

// A device has exactly one location: its policy_id fact.
type fixtureDevice struct{ id, serial, location string }

// Policy hierarchy comes from policiesFixture:
//
//	acme ─┬─ finance ── fin-laptops
//	      └─ sales
//	other
var fixtureDevices = []fixtureDevice{
	{"d1", "B-2", "acme"},
	{"d2", "A-1", "finance"},
	{"d3", "C-3", "fin-laptops"},
	{"d4", "D-4", "fin-laptops"},
	{"d5", "E-5", "sales"},
	{"d6", "F-6", ""}, // no location at all
	{"d9", "Z-9", "other"},
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
	totalOverride int // when set, reported in metadata.total instead of the real total
}

func newDeviceServer(t *testing.T) *deviceServer {
	t.Helper()
	ds := &deviceServer{}
	ds.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/oa/policies/query":
			io.WriteString(w, policiesFixture)
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

			var items []string
			for _, d := range list[start:end] {
				facts := fmt.Sprintf(`"serial_number":{"type":"string","value":%q}`, d.serial)
				if d.location != "" {
					facts += fmt.Sprintf(`,"policy_id":{"type":"string","value":%q}`, d.location)
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
