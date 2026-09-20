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
