// Package addigy is a thin, CLI-friendly layer over the generated Addigy API
// client in the gen subpackage.
//
// The generated client does the transport work (URLs, path parameters, the
// operations themselves). This package adds authentication, error handling
// and small "projection" types that decode only the response fields the CLI
// displays. Raw response JSON is preserved on every item, so nothing the API
// returns is lost when printing with --json.
package addigy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy/gen"
)

// DefaultBaseURL is the Addigy v2 API. The published spec has no host, so
// this is configurable.
const DefaultBaseURL = "https://api.addigy.com/api/v2"

// Options configures New.
type Options struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client // optional
	Debug      io.Writer    // optional: request lines are written here
}

// API wraps the generated client.
type API struct {
	c *gen.ClientWithResponses
}

// New creates an API client that authenticates with the x-api-key header.
func New(o Options) (*API, error) {
	if o.APIKey == "" {
		return nil, errors.New("addigy: an API key is required")
	}
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	debug := o.Debug
	apiKey := o.APIKey

	editor := func(_ context.Context, req *http.Request) error {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("Accept", "application/json")
		if debug != nil {
			fmt.Fprintf(debug, "> %s %s\n", req.Method, req.URL)
		}
		return nil
	}

	c, err := gen.NewClientWithResponses(base, gen.WithHTTPClient(hc), gen.WithRequestEditorFn(editor))
	if err != nil {
		return nil, err
	}
	return &API{c: c}, nil
}

// APIError is returned for any non-2xx response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("addigy API returned %d %s", e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		msg += " (check the API key and its permissions)"
	}
	return msg
}

func checkStatus(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	e := &APIError{Status: status}
	var er struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &er) == nil {
		e.Message = er.Message
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(body))
		if len(e.Message) > 300 {
			e.Message = e.Message[:300] + "…"
		}
	}
	return e
}

// ---- Devices ---------------------------------------------------------------

// DeviceQuery describes a Universal Search request (POST /devices).
type DeviceQuery struct {
	Search    string   // free-text search across facts
	PolicyID  string   // restrict to a policy
	Facts     []string // fact identifiers to return for each device
	SortField string
	Desc      bool
	Page      int
	PerPage   int
}

// PageMetadata is the pagination block Addigy returns with list responses.
type PageMetadata struct {
	Page        int `json:"page"`
	PageCount   int `json:"page_count"`
	PerPage     int `json:"per_page"`
	ResultCount int `json:"result_count"`
	Total       int `json:"total"`
}

// Fact is one device fact as returned by device search.
type Fact struct {
	Type     string `json:"type"`
	Value    any    `json:"value"`
	ErrorMsg string `json:"error_msg"`
}

// Device is the projection of a device audit the CLI displays.
type Device struct {
	AgentID        string          `json:"agentid"`
	OrgID          string          `json:"orgid"`
	AuditDate      string          `json:"audit_date"`
	AgentAuditDate string          `json:"agent_audit_date"`
	Facts          map[string]Fact `json:"facts"`
	Raw            json.RawMessage `json:"-"` // the full item exactly as returned
}

// DevicePage is one page of search results.
type DevicePage struct {
	Items    []Device
	Metadata PageMetadata
}

type deviceFilterBody struct {
	DesiredFactIdentifiers []string   `json:"desired_fact_identifiers,omitempty"`
	Page                   int        `json:"page,omitempty"`
	PerPage                int        `json:"per_page,omitempty"`
	Query                  *queryBody `json:"query,omitempty"`
	SortDirection          string     `json:"sort_direction,omitempty"`
	SortField              string     `json:"sort_field,omitempty"`
}

type queryBody struct {
	PolicyID  string `json:"policy_id,omitempty"`
	SearchAny string `json:"search_any,omitempty"`
}

func (q DeviceQuery) body() deviceFilterBody {
	b := deviceFilterBody{
		DesiredFactIdentifiers: q.Facts,
		Page:                   q.Page,
		PerPage:                q.PerPage,
		SortField:              q.SortField,
	}
	if q.SortField != "" {
		b.SortDirection = "asc"
		if q.Desc {
			b.SortDirection = "desc"
		}
	}
	if q.Search != "" || q.PolicyID != "" {
		b.Query = &queryBody{PolicyID: q.PolicyID, SearchAny: q.Search}
	}
	return b
}

// SearchDevices runs a Universal Search (POST /devices).
func (a *API) SearchDevices(ctx context.Context, q DeviceQuery) (*DevicePage, error) {
	body, err := json.Marshal(q.body())
	if err != nil {
		return nil, err
	}
	resp, err := a.c.GetDevicesWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}

	var raw struct {
		Items    []json.RawMessage `json:"items"`
		Metadata PageMetadata      `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, fmt.Errorf("decoding device search response: %w", err)
	}
	page := &DevicePage{Metadata: raw.Metadata}
	for _, r := range raw.Items {
		var d Device
		if err := json.Unmarshal(r, &d); err != nil {
			return nil, fmt.Errorf("decoding device: %w", err)
		}
		d.Raw = r
		page.Items = append(page.Items, d)
	}
	return page, nil
}

// DevicePolicyAssignments returns the IDs of the policies assigned to a
// device (GET /o/{organization_id}/devices/{agent_id}/policy-assignments).
func (a *API) DevicePolicyAssignments(ctx context.Context, orgID, agentID string) ([]string, error) {
	resp, err := a.c.GetDevicePolicyAssignmentsWithResponse(ctx, orgID, agentID)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(resp.Body, &ids); err != nil {
		return nil, fmt.Errorf("decoding policy assignments: %w", err)
	}
	return ids, nil
}

// ---- Policies --------------------------------------------------------------

// Policy is the projection of a policy the CLI displays.
type Policy struct {
	ID           string          `json:"policyId"`
	Name         string          `json:"name"`
	Parent       string          `json:"parent"`
	OrgID        string          `json:"orgid"`
	AgentVersion string          `json:"agent_version"`
	LastDeployed any             `json:"last_deployed"`
	CreationTime any             `json:"creation_time"`
	Raw          json.RawMessage `json:"-"` // the full policy exactly as returned
}

// QueryPolicies fetches policies (POST /oa/policies/query). With no ids it
// returns all policies of the organization.
func (a *API) QueryPolicies(ctx context.Context, ids []string) ([]Policy, error) {
	req := map[string]any{}
	if len(ids) > 0 {
		req["policies"] = ids
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := a.c.GetPoliciesWithBodyWithResponse(ctx, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}

	var raws []json.RawMessage
	if err := json.Unmarshal(resp.Body, &raws); err != nil {
		return nil, fmt.Errorf("decoding policies: %w", err)
	}
	pols := make([]Policy, 0, len(raws))
	for _, r := range raws {
		var p Policy
		if err := json.Unmarshal(r, &p); err != nil {
			return nil, fmt.Errorf("decoding policy: %w", err)
		}
		p.Raw = r
		pols = append(pols, p)
	}
	return pols, nil
}

// ---- Facts -----------------------------------------------------------------

// FactDef describes a fact that can be queried on devices.
type FactDef struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	ReturnType string `json:"return_type"`
	Provider   string `json:"provider"`
	Source     string `json:"source"`
	Notes      string `json:"notes"`
}

// AvailableFacts lists built-in and custom facts
// (GET /o/{organization_id}/facts).
func (a *API) AvailableFacts(ctx context.Context, orgID string) ([]FactDef, error) {
	resp, err := a.c.GetAvailableFactsWithResponse(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	var facts []FactDef
	if err := json.Unmarshal(resp.Body, &facts); err != nil {
		return nil, fmt.Errorf("decoding facts: %w", err)
	}
	return facts, nil
}
