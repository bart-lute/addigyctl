package addigy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestAPI(t *testing.T, h http.HandlerFunc) *API {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	api, err := New(Options{BaseURL: srv.URL + "/api/v2", APIKey: "secret-key"})
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func TestSearchDevices(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/devices" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "secret-key" {
			t.Errorf("x-api-key = %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		if body["page"] != float64(2) || body["sort_direction"] != "desc" || body["sort_field"] != "serial_number" {
			t.Errorf("unexpected body: %s", b)
		}
		q, _ := body["query"].(map[string]any)
		if q["search_any"] != "mbp" {
			t.Errorf("unexpected query: %s", b)
		}
		io.WriteString(w, `{"items":[{"agentid":"a1","orgid":"o1","facts":{"serial_number":{"type":"string","value":"C02XYZ"}}}],
			"metadata":{"page":2,"page_count":3,"per_page":1,"result_count":1,"total":3}}`)
	})

	page, err := api.SearchDevices(context.Background(), DeviceQuery{
		Search: "mbp", Page: 2, PerPage: 1, SortField: "serial_number", Desc: true,
		Facts: []string{"serial_number"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].AgentID != "a1" {
		t.Fatalf("unexpected items: %+v", page.Items)
	}
	if v := page.Items[0].Facts["serial_number"].Value; v != "C02XYZ" {
		t.Errorf("serial = %v", v)
	}
	if !strings.Contains(string(page.Items[0].Raw), `"agentid":"a1"`) {
		t.Errorf("raw item not preserved: %s", page.Items[0].Raw)
	}
	if page.Metadata.PageCount != 3 || page.Metadata.Total != 3 {
		t.Errorf("metadata = %+v", page.Metadata)
	}
}

func TestQueryPolicies(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/oa/policies/query" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if strings.TrimSpace(string(b)) != `{}` {
			t.Errorf("expected empty request for all policies, got %s", b)
		}
		io.WriteString(w, `[{"policyId":"p1","name":"Base","parent":"","orgid":"o1","last_deployed":"2026-09-01"}]`)
	})
	pols, err := api.QueryPolicies(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pols) != 1 || pols[0].ID != "p1" || pols[0].OrgID != "o1" || len(pols[0].Raw) == 0 {
		t.Fatalf("unexpected policies: %+v", pols)
	}
}

func TestDevicePolicyAssignments(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/o/org1/devices/agent1/policy-assignments" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		io.WriteString(w, `["p1","p2"]`)
	})
	ids, err := api.DevicePolicyAssignments(context.Background(), "org1", "agent1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[1] != "p2" {
		t.Errorf("ids = %v", ids)
	}
}

func TestAvailableFacts(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/o/org1/facts" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		io.WriteString(w, `[{"identifier":"serial_number","name":"Serial Number","return_type":"string"}]`)
	})
	facts, err := api.AvailableFacts(context.Background(), "org1")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Identifier != "serial_number" {
		t.Errorf("facts = %+v", facts)
	}
}

func TestSearchAlerts(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/oa/monitoring/alerts/query" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		if body["page"] != float64(1) || body["per_page"] != float64(20) {
			t.Errorf("unexpected paging: %s", b)
		}
		// AlertQuery.Desc=true should send the API's "asc", the inverted
		// direction that actually returns the newest/highest first.
		if body["sort_field"] != "created_date" || body["sort_direction"] != "asc" {
			t.Errorf("unexpected sort: %s", b)
		}
		q, _ := body["query"].(map[string]any)
		statuses, _ := q["statuses"].([]any)
		if len(statuses) != 1 || statuses[0] != "Unattended" {
			t.Errorf("unexpected query: %s", b)
		}
		// ticket_id has been observed as a number on old alerts, not just a
		// string or null; Alert.TicketID must tolerate that.
		io.WriteString(w, `{"items":[{"id":"a1","name":"Disk full","status":"Unattended","agent_id":"dev1","muted":true,"ticket_id":42}],
			"metadata":{"page":1,"page_count":1,"per_page":20,"result_count":1,"total":1}}`)
	})

	page, err := api.SearchAlerts(context.Background(), AlertQuery{
		Statuses: []string{"Unattended"}, SortField: "created_date", Desc: true, Page: 1, PerPage: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "a1" || !page.Items[0].Muted {
		t.Fatalf("unexpected items: %+v", page.Items)
	}
	if v, ok := page.Items[0].TicketID.(float64); !ok || v != 42 {
		t.Errorf("ticket_id = %#v, want float64(42)", page.Items[0].TicketID)
	}
	if page.Metadata.Total != 1 {
		t.Errorf("metadata = %+v", page.Metadata)
	}
}

func TestSearchEvents(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/events/query" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		if body["from_date_time"] != "2026-01-01T00:00:00Z" || body["to_date_time"] != "2026-01-02T00:00:00Z" {
			t.Errorf("unexpected time range: %s", b)
		}
		// Unlike alerts, this endpoint's sort_direction is not inverted:
		// EventQuery.Desc=true sends the API's own "desc" directly.
		if body["sort_direction"] != "desc" {
			t.Errorf("unexpected sort direction: %s", b)
		}
		queries, _ := body["queries"].([]any)
		if len(queries) != 1 {
			t.Fatalf("expected one query filter, got %s", b)
		}
		q0, _ := queries[0].(map[string]any)
		fields, _ := q0["fields"].([]any)
		if len(fields) != 1 || fields[0] != "level" || q0["query"] != "warning" {
			t.Errorf("unexpected query filter: %s", b)
		}
		io.WriteString(w, `{"items":[{"event_id":"e1","level":"warning","date":"2026-01-01T12:00:00Z",
			"action":{"name":"executed","details":"did a thing"},
			"action_sender":{"type":"device","identifier":"d1","name":"Some Mac"},
			"action_receiver":{"type":"platform","identifier":"addigy-mdm","name":"Addigy MDM"},
			"result":{"status":"success"}}],
			"metadata":{"page":1,"page_count":1,"per_page":20,"result_count":1,"total":1}}`)
	})

	page, err := api.SearchEvents(context.Background(), EventQuery{
		From: "2026-01-01T00:00:00Z", To: "2026-01-02T00:00:00Z",
		Level: "warning", Desc: true, Page: 1, PerPage: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "e1" || page.Items[0].Sender.Name != "Some Mac" {
		t.Fatalf("unexpected items: %+v", page.Items)
	}
	if page.Metadata.Total != 1 {
		t.Errorf("metadata = %+v", page.Metadata)
	}
}

func TestSearchSmartSoftware(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/oa/smart-software/query" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		if body["page"] != float64(1) || body["per_page"] != float64(50) || body["sort_field"] != "name" || body["sort_direction"] != "desc" {
			t.Errorf("unexpected body: %s", b)
		}
		q, _ := body["query"].(map[string]any)
		if q["name_contains"] != "zoom" || q["archived"] != false || q["identifier"] != nil {
			t.Errorf("unexpected query: %s", b)
		}
		io.WriteString(w, `{"items":[{"identifier":"Zoom-u1","instruction_id":"i2","base_identifier":"Zoom","name":"Zoom (6.1)",
			"version":"6.1","archived":false,"installation_script":"echo hi",
			"downloads":[{"id":"f1","filename":"zoom.pkg","size":42,"md5_hash":"abc"}]}],
			"metadata":{"page":1,"page_count":1,"per_page":50,"result_count":1,"total":1}}`)
	})

	archived := false
	page, err := api.SearchSmartSoftware(context.Background(), SmartSoftwareQuery{
		NameContains: "zoom", Archived: &archived, Desc: true, PerPage: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("unexpected items: %+v", page.Items)
	}
	s := page.Items[0]
	if s.Identifier != "Zoom-u1" || s.InstructionID != "i2" || s.Version != "6.1" {
		t.Errorf("item = %+v", s)
	}
	if len(s.Downloads) != 1 || s.Downloads[0].ID != "f1" || s.Downloads[0].Size != 42 {
		t.Errorf("downloads = %+v", s.Downloads)
	}
	if !strings.Contains(string(s.Raw), `"installation_script":"echo hi"`) {
		t.Errorf("raw item not preserved: %s", s.Raw)
	}
}

func TestSmartSoftwareNotFound(t *testing.T) {
	// Addigy reports an unknown id as a 500 with "not found" deep in the chain.
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/o/o1/smart-software/nope" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"message":"error getting smart software","error_chain":{"message":"error getting custom software",
			"error_chain":{"message":"","internal_message":"custom software not found"}}}`)
	})
	_, err := api.SmartSoftware(context.Background(), "o1", "nope")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("expected a 404 naming the id, got %v", err)
	}
}

func TestNewSmartSoftwareVersion(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/o/o1/smart-software/i1/new-version" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"version":"2.0"}` {
			t.Errorf("body not sent as given: %s", b)
		}
		io.WriteString(w, `{"identifier":"X-u1","instruction_id":"i2","version":"2.0"}`)
	})
	s, err := api.NewSmartSoftwareVersion(context.Background(), "o1", "i1", json.RawMessage(`{"version":"2.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.InstructionID != "i2" || len(s.Raw) == 0 {
		t.Errorf("created version = %+v", s)
	}
}

func TestSearchFiles(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/oa/files/query" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		// page is always sent: the endpoint 400s without it.
		if body["page"] != float64(1) || body["sort_field"] != "created" || body["sort_direction"] != "desc" {
			t.Errorf("unexpected body: %s", b)
		}
		if h, _ := body["md5_hash"].([]any); len(h) != 1 || h[0] != "abc" {
			t.Errorf("unexpected md5_hash: %s", b)
		}
		if _, ok := body["search_term"]; ok {
			t.Errorf("search_term sent though empty: %s", b)
		}
		io.WriteString(w, `{"items":[{"id":"f1","filename":"zoom.pkg","size":42,"md5_hash":"abc","created":"2026-08-13T12:57:17Z"}],
			"metadata":{"page":1,"page_count":1,"per_page":10,"result_count":1,"total":1}}`)
	})
	page, err := api.SearchFiles(context.Background(), FileQuery{MD5Hashes: []string{"abc"}, SortField: "created", Desc: true, PerPage: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Filename != "zoom.pkg" || page.Metadata.Total != 1 {
		t.Errorf("unexpected page: %+v", page)
	}
}

func TestFile(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/oa/files/f1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"id":"f1","filename":"zoom.pkg","size":42}`)
	})
	f, err := api.File(context.Background(), "f1")
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != "f1" || f.Filename != "zoom.pkg" || f.Size != 42 {
		t.Errorf("file = %+v", f)
	}
}

func TestErrorMapping(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"code":403,"message":"missing permission: View Devices"}`)
	})
	_, err := api.SearchDevices(context.Background(), DeviceQuery{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != 403 || !strings.Contains(err.Error(), "View Devices") || !strings.Contains(err.Error(), "API key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewRequiresKey(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("expected an error without an API key")
	}
}

func TestEmptyPagesAreEmptyLists(t *testing.T) {
	// Addigy returns "items": null when nothing matches; -j must print [].
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":null,"metadata":{"page":1,"page_count":0,"per_page":10,"result_count":0,"total":0}}`)
	})
	ctx := context.Background()
	files, err := api.SearchFiles(ctx, FileQuery{MD5Hashes: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := api.SearchAlerts(ctx, AlertQuery{})
	if err != nil {
		t.Fatal(err)
	}
	events, err := api.SearchEvents(ctx, EventQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"files": files.Items, "alerts": alerts.Items, "events": events.Items} {
		if b, _ := json.Marshal(v); string(b) != "[]" {
			t.Errorf("%s: no results marshal as %s, want []", name, b)
		}
	}
}
