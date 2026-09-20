package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fixtureAlert struct {
	id, name, status, agentID, level, category string
	muted                                      bool
}

var fixtureAlerts = []fixtureAlert{
	{"a1", "Disk almost full", "Unattended", "dev1", "critical", "Storage", false},
	{"a2", "Battery health low", "Unattended", "dev2", "warning", "Hardware", false},
	{"a3", "Old malware alert", "Unattended", "dev-gone", "warning", "Security", false},
	{"a4", "Muted noisy check", "Acknowledged", "dev1", "warning", "General", true},
	{"a5", "Closed ticket", "Resolved", "dev2", "critical", "Security", false},
}

// fixtureAlertDevices resolves agent IDs to a serial number and device name;
// "dev-gone" is deliberately absent, like a decommissioned device.
var fixtureAlertDevices = map[string]struct{ serial, name string }{
	"dev1": {"SER-1", "Alice's Mac"},
	"dev2": {"SER-2", "Bob's Mac"},
}

type alertsRequest struct {
	page, perPage int
	sortField     string
	sortDirection string
	statuses      []string
}

type alertsServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []alertsRequest
}

func newAlertsServer(t *testing.T) *alertsServer {
	t.Helper()
	as := &alertsServer{}
	as.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/devices":
			var body struct {
				Page    int `json:"page"`
				PerPage int `json:"per_page"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			var items []string
			for id, d := range fixtureAlertDevices {
				items = append(items, fmt.Sprintf(
					`{"agentid":%q,"facts":{"serial_number":{"type":"string","value":%q},"device_name":{"type":"string","value":%q}}}`,
					id, d.serial, d.name))
			}
			fmt.Fprintf(w, `{"items":[%s],"metadata":{"page":1,"page_count":1,"per_page":50,"result_count":%d,"total":%d}}`,
				strings.Join(items, ","), len(items), len(items))

		case "/api/v2/oa/monitoring/alerts/query":
			var body struct {
				Page          int    `json:"page"`
				PerPage       int    `json:"per_page"`
				SortField     string `json:"sort_field"`
				SortDirection string `json:"sort_direction"`
				Query         struct {
					Statuses []string `json:"statuses"`
				} `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("bad request body: %v", err)
			}
			as.mu.Lock()
			as.requests = append(as.requests, alertsRequest{
				page: body.Page, perPage: body.PerPage,
				sortField: body.SortField, sortDirection: body.SortDirection,
				statuses: body.Query.Statuses,
			})
			as.mu.Unlock()

			list := fixtureAlerts
			if len(body.Query.Statuses) > 0 {
				want := map[string]bool{}
				for _, s := range body.Query.Statuses {
					want[s] = true
				}
				var filtered []fixtureAlert
				for _, a := range list {
					if want[a.status] {
						filtered = append(filtered, a)
					}
				}
				list = filtered
			}

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
			for _, a := range list[start:end] {
				items = append(items, fmt.Sprintf(
					`{"id":%q,"name":%q,"status":%q,"agent_id":%q,"level":%q,"category":%q,"muted":%v,"created_date":"2026-01-01T00:00:00Z"}`,
					a.id, a.name, a.status, a.agentID, a.level, a.category, a.muted))
			}
			pageCount := (len(list) + per - 1) / per
			if pageCount == 0 {
				pageCount = 1
			}
			fmt.Fprintf(w, `{"items":[%s],"metadata":{"page":%d,"page_count":%d,"per_page":%d,"result_count":%d,"total":%d}}`,
				strings.Join(items, ","), page, pageCount, per, end-start, len(list))

		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(as.Close)
	return as
}

func (as *alertsServer) run(t *testing.T, cmd *AlertsListCmd) runResult {
	t.Helper()
	var out, errOut bytes.Buffer
	app := &App{
		Ctx: context.Background(),
		G:   &Globals{APIKey: "k", BaseURL: as.URL + "/api/v2"},
		Out: &out, Err: &errOut,
	}
	err := cmd.Run(app)
	return runResult{out.String(), errOut.String(), err}
}

func TestAlertsListDefaultStatusesAndSort(t *testing.T) {
	as := newAlertsServer(t)
	res := as.run(t, &AlertsListCmd{PerPage: 50})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if len(as.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(as.requests))
	}
	req := as.requests[0]
	if req.sortField != "created_date" || req.sortDirection != "asc" {
		t.Errorf("default sort = %+v, want created_date/asc (the inverted, newest-first direction)", req)
	}
	got := map[string]bool{}
	for _, s := range req.statuses {
		got[s] = true
	}
	if len(got) != 2 || !got["Unattended"] || !got["Acknowledged"] {
		t.Errorf("default statuses = %v, want Unattended+Acknowledged", req.statuses)
	}
	// Resolved alert a5 must not appear by default.
	if strings.Contains(res.out, "Closed ticket") {
		t.Errorf("resolved alert leaked into default output:\n%s", res.out)
	}
	if !strings.Contains(res.out, "SER-1") || !strings.Contains(res.out, "Alice's Mac") {
		t.Errorf("expected device identity resolved via agent_id:\n%s", res.out)
	}
	// dev-gone has no matching device: falls back to "-".
	if !strings.Contains(res.out, "Old malware alert") {
		t.Errorf("expected alert with unresolved device present:\n%s", res.out)
	}
}

func TestAlertsListAllStatusesSendsNoFilter(t *testing.T) {
	as := newAlertsServer(t)
	res := as.run(t, &AlertsListCmd{All: true, PerPage: 50})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if len(as.requests[0].statuses) != 0 {
		t.Errorf("--all should send no status filter, got %v", as.requests[0].statuses)
	}
	if !strings.Contains(res.out, "Closed ticket") {
		t.Errorf("--all should include resolved alerts:\n%s", res.out)
	}
}

// TestAlertsListDescReversesCreatedsDefault checks that --desc, on the
// default "created" column (whose own default is newest first), reverses to
// oldest first rather than doubling down on newest-first.
func TestAlertsListDescReversesCreatedsDefault(t *testing.T) {
	as := newAlertsServer(t)
	res := as.run(t, &AlertsListCmd{Desc: true, PerPage: 50})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if got := as.requests[len(as.requests)-1].sortDirection; got != "desc" {
		t.Errorf("--desc on created should send API sort_direction=desc (oldest first), got %q", got)
	}
}

// TestAlertsListDescOnNameSortsDescending checks that --desc on a column
// whose own default is ascending (unlike created) behaves conventionally:
// it sorts descending.
func TestAlertsListDescOnNameSortsDescending(t *testing.T) {
	as := newAlertsServer(t)
	res := as.run(t, &AlertsListCmd{Sort: "name", Desc: true, PerPage: 50})
	if res.err != nil {
		t.Fatal(res.err)
	}
	// Addigy's sort_direction is inverted from its label for this endpoint
	// (verified in TestSearchAlerts), so descending is sent as "asc".
	if got := as.requests[len(as.requests)-1].sortDirection; got != "asc" {
		t.Errorf("--sort name --desc should send API sort_direction=asc, got %q", got)
	}
}

func TestAlertsListMutedFiltersClientSideAcrossPages(t *testing.T) {
	as := newAlertsServer(t)
	res := as.run(t, &AlertsListCmd{Muted: true, All: true, PerPage: 50})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if !strings.Contains(res.out, "Muted noisy check") {
		t.Errorf("expected the one muted alert:\n%s", res.out)
	}
	if strings.Contains(res.out, "Disk almost full") {
		t.Errorf("non-muted alert should be filtered out:\n%s", res.out)
	}
	if !strings.Contains(res.out, "1 of 1 alerts") {
		t.Errorf("expected pagination metadata over the filtered set, got:\n%s", res.out)
	}
}

func TestAlertsListKnownDevicesFiltersOrphanedAlerts(t *testing.T) {
	as := newAlertsServer(t)
	res := as.run(t, &AlertsListCmd{KnownDevices: true, All: true, PerPage: 50})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if strings.Contains(res.out, "Old malware alert") {
		t.Errorf("alert for a nonexistent device (dev-gone) should be filtered out:\n%s", res.out)
	}
	for _, name := range []string{"Disk almost full", "Battery health low", "Closed ticket"} {
		if !strings.Contains(res.out, name) {
			t.Errorf("expected alert for a known device to remain: %q\n%s", name, res.out)
		}
	}
	if !strings.Contains(res.out, "4 of 4 alerts") {
		t.Errorf("expected pagination metadata over the filtered set, got:\n%s", res.out)
	}
}

func TestAlertsListUnknownSortColumn(t *testing.T) {
	as := newAlertsServer(t)
	res := as.run(t, &AlertsListCmd{Sort: "bogus"})
	if res.err == nil || !strings.Contains(res.err.Error(), `unknown --sort column "bogus"`) {
		t.Errorf("got %v", res.err)
	}
}

func TestAlertsListJSON(t *testing.T) {
	as := newAlertsServer(t)
	var out bytes.Buffer
	app := &App{
		Ctx: context.Background(),
		G:   &Globals{APIKey: "k", BaseURL: as.URL + "/api/v2", JSON: true},
		Out: &out, Err: &bytes.Buffer{},
	}
	if err := (&AlertsListCmd{PerPage: 50}).Run(app); err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if len(items) == 0 {
		t.Fatal("expected at least one alert")
	}
}

func TestAlertsListCSV(t *testing.T) {
	as := newAlertsServer(t)
	var out bytes.Buffer
	app := &App{
		Ctx: context.Background(),
		G:   &Globals{APIKey: "k", BaseURL: as.URL + "/api/v2", Output: "csv"},
		Out: &out, Err: &bytes.Buffer{},
	}
	if err := (&AlertsListCmd{PerPage: 50}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "LEVEL,NAME,CATEGORY,SERIAL NUMBER,DEVICE NAME,STATUS,MUTED,CREATED\n") {
		t.Errorf("unexpected CSV header:\n%s", out.String())
	}
	if strings.Contains(out.String(), "alerts\n") {
		t.Errorf("CSV must not contain a footer:\n%s", out.String())
	}
}
