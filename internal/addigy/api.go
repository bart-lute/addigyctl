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

// nonNil makes a page with no results an empty list rather than nil, so it
// prints as [] in JSON: Addigy returns "items": null for none.
func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
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

// ---- ADE tokens -------------------------------------------------------------

// AdeToken is the projection of an Automated Device Enrollment (ADE) token
// the CLI displays.
type AdeToken struct {
	PolicyID             string `json:"policy_id"`
	OrgID                string `json:"orgid"`
	AccessTokenExpiry    string `json:"access_token_expiry"`
	LastScanTime         string `json:"last_scan_time"`
	Disabled             bool   `json:"disabled"`
	Removed              bool   `json:"removed"`
	DevicesSyncCompleted bool   `json:"devices_sync_completed"`
	SyncingError         string `json:"syncing_error"`
}

// AdeTokens lists the ADE tokens assigned to policies
// (POST /oa/ade/tokens/policies/query). With no ids it returns every token.
func (a *API) AdeTokens(ctx context.Context, policyIDs []string) ([]AdeToken, error) {
	req := gen.AdeAutomaticEnrollmentRequest{}
	if len(policyIDs) > 0 {
		req.PolicyIds = &policyIDs
	}
	resp, err := a.c.GetAdeTokensWithResponse(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	var tokens []AdeToken
	if err := json.Unmarshal(resp.Body, &tokens); err != nil {
		return nil, fmt.Errorf("decoding ade tokens: %w", err)
	}
	return tokens, nil
}

// ---- Alerts -----------------------------------------------------------------

// Alert is a received alert (a triggered instance of an alert policy), as
// the CLI displays it.
type Alert struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Status            string   `json:"status"` // "Unattended", "Acknowledged" or "Resolved"
	AgentID           string   `json:"agent_id"`
	Level             string   `json:"level"`
	Category          string   `json:"category"`
	FactIdentifier    string   `json:"fact_identifier"`
	Value             any      `json:"value"`
	ValueType         string   `json:"value_type"`
	Selector          string   `json:"selector"`
	Emails            []string `json:"emails"`
	RemediationStatus string   `json:"remediation_status"`
	CreatedDate       string   `json:"created_date"`
	AckDate           string   `json:"ack_date"`
	ResolvedDate      string   `json:"resolved_date"`
	ResolvedUserEmail string   `json:"resolved_user_email"`
	Muted             bool     `json:"muted"`
	MutedForDays      int      `json:"muted_for_days"`
	TicketID          any      `json:"ticket_id"` // usually a string or null, but seen as a number on old alerts
}

// AlertPage is one page of received-alerts results.
type AlertPage struct {
	Items    []Alert
	Metadata PageMetadata
}

// AlertQuery selects, sorts and paginates the received alerts SearchAlerts
// returns. Statuses is any of "Unattended", "Acknowledged" or "Resolved"; a
// nil/empty Statuses matches every status. There is no server-side way to
// filter by the Muted flag; callers needing that must filter client-side.
type AlertQuery struct {
	Statuses      []string
	Category      string
	NameContains  string
	SortField     string // e.g. "created_date", "name", "level", "status", "category"
	Desc          bool
	Page, PerPage int
}

func (q AlertQuery) body() gen.AlertEntitiesPaginatedReceivedAlertsRequestQuery {
	// sort_field and sort_direction look optional in Addigy's spec but the
	// endpoint 400s without them, so always send a default.
	sortField := q.SortField
	if sortField == "" {
		sortField = "created_date"
	}
	// Addigy's sort_direction for this endpoint is inverted from its label
	// ("asc" returns the newest/highest first, "desc" the oldest/lowest),
	// verified against a live tenant. Flip it here so AlertQuery.Desc means
	// what callers expect: ascending (oldest first) by default, descending
	// (newest first) with Desc.
	dir := gen.AlertEntitiesPaginatedReceivedAlertsRequestQuerySortDirectionDesc
	if q.Desc {
		dir = gen.AlertEntitiesPaginatedReceivedAlertsRequestQuerySortDirectionAsc
	}
	b := gen.AlertEntitiesPaginatedReceivedAlertsRequestQuery{
		Page:          &q.Page,
		PerPage:       &q.PerPage,
		SortField:     &sortField,
		SortDirection: &dir,
	}
	if len(q.Statuses) > 0 || q.Category != "" || q.NameContains != "" {
		f := gen.AlertEntitiesFilter{}
		if len(q.Statuses) > 0 {
			f.Statuses = &q.Statuses
		}
		if q.Category != "" {
			f.Category = &q.Category
		}
		if q.NameContains != "" {
			f.NameContains = &q.NameContains
		}
		b.Query = &f
	}
	return b
}

// SearchAlerts runs a filtered, sorted, paginated received-alerts query
// (POST /oa/monitoring/alerts/query).
func (a *API) SearchAlerts(ctx context.Context, q AlertQuery) (*AlertPage, error) {
	resp, err := a.c.GetReceivedAlertsByFilterWithResponse(ctx, q.body())
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	var raw struct {
		Items    []Alert      `json:"items"`
		Metadata PageMetadata `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, fmt.Errorf("decoding alerts: %w", err)
	}
	return &AlertPage{Items: nonNil(raw.Items), Metadata: raw.Metadata}, nil
}

// ---- Events -------------------------------------------------------------

// EventActor is who or what sent, received, or is the subject of an event:
// a device, a user, an API key, the Addigy platform itself, and so on.
type EventActor struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
}

// EventAction is what happened in an event.
type EventAction struct {
	Name    string      `json:"name"`
	Details string      `json:"details"`
	Entity  *EventActor `json:"entity"`
}

// EventResult is the outcome of an event's action.
type EventResult struct {
	Status  string `json:"status"`
	Details string `json:"details"`
}

// Event is a system event (Addigy's audit log entry): who/what did what to
// what, and whether it succeeded.
type Event struct {
	ID       string      `json:"event_id"`
	Level    string      `json:"level"`
	Date     string      `json:"date"`
	Source   string      `json:"source"`
	Action   EventAction `json:"action"`
	Sender   EventActor  `json:"action_sender"`
	Receiver EventActor  `json:"action_receiver"`
	Result   EventResult `json:"result"`
}

// EventPage is one page of system-events results.
type EventPage struct {
	Items    []Event
	Metadata PageMetadata
}

// EventQuery selects, sorts and paginates the events SearchEvents returns.
// From and To are required by the endpoint (RFC3339); it returns no results
// without them. Level and Action are free-text filters against the "level"
// and "action.name" fields respectively.
type EventQuery struct {
	From, To      string
	Level         string
	Action        string
	Desc          bool
	Page, PerPage int
}

func (q EventQuery) body() gen.SystemEventsSearchRequestQuery {
	dir := "asc"
	if q.Desc {
		dir = "desc"
	}
	b := gen.SystemEventsSearchRequestQuery{
		FromDateTime:  &q.From,
		ToDateTime:    &q.To,
		Page:          &q.Page,
		PerPage:       &q.PerPage,
		SortDirection: &dir,
	}
	var queries []gen.EventsClientQuery
	if q.Level != "" {
		queries = append(queries, gen.EventsClientQuery{Fields: &[]string{"level"}, Query: &q.Level})
	}
	if q.Action != "" {
		queries = append(queries, gen.EventsClientQuery{Fields: &[]string{"action.name"}, Query: &q.Action})
	}
	if len(queries) > 0 {
		b.Queries = &queries
	}
	return b
}

// SearchEvents runs a filtered, sorted, paginated system-events query
// (POST /events/query). Unlike SearchAlerts, this endpoint's sort_direction
// is not inverted: "asc" is oldest first, "desc" is newest first, verified
// against a live tenant.
func (a *API) SearchEvents(ctx context.Context, q EventQuery) (*EventPage, error) {
	resp, err := a.c.GetSystemEventsWithResponse(ctx, q.body())
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	var raw struct {
		Items    []Event      `json:"items"`
		Metadata PageMetadata `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, fmt.Errorf("decoding events: %w", err)
	}
	return &EventPage{Items: nonNil(raw.Items), Metadata: raw.Metadata}, nil
}

// ---- Smart Software ---------------------------------------------------------

// SmartSoftware is one version of a Smart Software item, as the CLI displays
// it. All versions of an item share Identifier ("<base>-<uuid>");
// InstructionID is unique per version and is what SmartSoftware (GET by id)
// takes. BaseIdentifier is the item's name without a version suffix.
type SmartSoftware struct {
	Identifier     string          `json:"identifier"`
	InstructionID  string          `json:"instruction_id"`
	BaseIdentifier string          `json:"base_identifier"`
	Name           string          `json:"name"`
	Version        any             `json:"version"` // a string in practice, but untyped in Addigy's spec
	Category       string          `json:"category"`
	Archived       bool            `json:"archived"`
	Downloads      []File          `json:"downloads"`
	Raw            json.RawMessage `json:"-"` // the full item exactly as returned
}

// SmartSoftwarePage is one page of Smart Software results.
type SmartSoftwarePage struct {
	Items    []SmartSoftware
	Metadata PageMetadata
}

// SmartSoftwareQuery selects, sorts and paginates the Smart Software versions
// SearchSmartSoftware returns. Every version is its own result; Identifier
// selects all versions of one item. A nil Archived matches both archived and
// active versions.
type SmartSoftwareQuery struct {
	Identifier    string
	NameContains  string
	Archived      *bool
	SortField     string // e.g. "name", "base_identifier"
	Desc          bool
	Page, PerPage int // PerPage is at most 100
}

func (q SmartSoftwareQuery) body() gen.SmartSoftwareSmartSoftwareQueryRequest {
	// page, per_page, sort_field and sort_direction are all required.
	sortField := q.SortField
	if sortField == "" {
		sortField = "name"
	}
	// Unlike alerts, this endpoint's sort_direction is not inverted, verified
	// against a live tenant.
	dir := gen.SmartSoftwareSmartSoftwareQueryRequestSortDirectionAsc
	if q.Desc {
		dir = gen.SmartSoftwareSmartSoftwareQueryRequestSortDirectionDesc
	}
	b := gen.SmartSoftwareSmartSoftwareQueryRequest{
		Page:          max(q.Page, 1),
		PerPage:       q.PerPage,
		SortField:     sortField,
		SortDirection: dir,
	}
	if q.Identifier != "" || q.NameContains != "" || q.Archived != nil {
		f := gen.SmartSoftwareFilter{Archived: q.Archived}
		if q.Identifier != "" {
			f.Identifier = &q.Identifier
		}
		if q.NameContains != "" {
			f.NameContains = &q.NameContains
		}
		b.Query = &f
	}
	return b
}

// SearchSmartSoftware runs a filtered, sorted, paginated Smart Software query
// (POST /oa/smart-software/query).
func (a *API) SearchSmartSoftware(ctx context.Context, q SmartSoftwareQuery) (*SmartSoftwarePage, error) {
	resp, err := a.c.GetSmartSoftwareItemsWithResponse(ctx, q.body())
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
		return nil, fmt.Errorf("decoding smart software: %w", err)
	}
	page := &SmartSoftwarePage{Items: make([]SmartSoftware, 0, len(raw.Items)), Metadata: raw.Metadata}
	for _, r := range raw.Items {
		s, err := decodeSmartSoftware(r)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, *s)
	}
	return page, nil
}

// SmartSoftware fetches one Smart Software version by its instruction ID
// (GET /o/{organization_id}/smart-software/{id}).
func (a *API) SmartSoftware(ctx context.Context, orgID, instructionID string) (*SmartSoftware, error) {
	resp, err := a.c.GetSmartSoftwareWithResponse(ctx, orgID, instructionID)
	if err != nil {
		return nil, err
	}
	// An unknown id is a 500 whose nested error chain says "not found";
	// report it as the 404 it is.
	if resp.StatusCode() == http.StatusInternalServerError && bytes.Contains(resp.Body, []byte("custom software not found")) {
		return nil, &APIError{Status: http.StatusNotFound, Message: fmt.Sprintf("no smart software with instruction id %q", instructionID)}
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	return decodeSmartSoftware(resp.Body)
}

// NewSmartSoftwareVersion creates a new version of the item whose current
// version has the given instruction ID
// (POST /o/{organization_id}/smart-software/{id}/new-version). body is the
// new version as a smart_software.UpdateSmartSoftwareRequest; the response is
// the created version.
func (a *API) NewSmartSoftwareVersion(ctx context.Context, orgID, instructionID string, body json.RawMessage) (*SmartSoftware, error) {
	resp, err := a.c.CreateSmartSoftwareNewVersionWithBodyWithResponse(ctx, orgID, instructionID, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	return decodeSmartSoftware(resp.Body)
}

// DeleteSmartSoftware deletes one Smart Software version by its instruction ID
// (DELETE /o/{organization_id}/smart-software/{id}). The files it downloads
// stay in the organization's file storage.
func (a *API) DeleteSmartSoftware(ctx context.Context, orgID, instructionID string) error {
	resp, err := a.c.DeleteSmartSoftwareWithResponse(ctx, orgID, instructionID)
	if err != nil {
		return err
	}
	return checkStatus(resp.StatusCode(), resp.Body)
}

func decodeSmartSoftware(r json.RawMessage) (*SmartSoftware, error) {
	var s SmartSoftware
	if err := json.Unmarshal(r, &s); err != nil {
		return nil, fmt.Errorf("decoding smart software: %w", err)
	}
	s.Raw = r
	return &s, nil
}

// ---- Files ------------------------------------------------------------------

// File is a file uploaded to the organization's Addigy file storage, such as
// a Smart Software installer.
type File struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	MD5Hash     string `json:"md5_hash"`
	Created     string `json:"created"`
	UserEmail   string `json:"user_email"`
	Provider    string `json:"provider"`
}

// FilePage is one page of file results.
type FilePage struct {
	Items    []File
	Metadata PageMetadata
}

// FileQuery selects, sorts and paginates the files SearchFiles returns.
// SearchTerm searches file names, case-insensitively and loosely: Addigy also
// returns near matches.
type FileQuery struct {
	IDs           []string
	MD5Hashes     []string
	SearchTerm    string
	SortField     string // e.g. "created", "filename", "size"
	Desc          bool
	Page, PerPage int // PerPage is at most 100
}

func (q FileQuery) body() gen.FilesOrganizationFilesRequest {
	// The endpoint 400s without a page. sort_direction is not inverted.
	page := max(q.Page, 1)
	b := gen.FilesOrganizationFilesRequest{Page: &page, PerPage: &q.PerPage}
	if q.SortField != "" {
		dir := "asc"
		if q.Desc {
			dir = "desc"
		}
		b.SortField = &q.SortField
		b.SortDirection = &dir
	}
	if len(q.IDs) > 0 {
		b.Ids = &q.IDs
	}
	if len(q.MD5Hashes) > 0 {
		b.Md5Hash = &q.MD5Hashes
	}
	if q.SearchTerm != "" {
		b.SearchTerm = &q.SearchTerm
	}
	return b
}

// SearchFiles runs a filtered, sorted, paginated file query
// (POST /oa/files/query).
func (a *API) SearchFiles(ctx context.Context, q FileQuery) (*FilePage, error) {
	resp, err := a.c.GetOrganizationFilesWithResponse(ctx, q.body())
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	var raw struct {
		Items    []File       `json:"items"`
		Metadata PageMetadata `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, fmt.Errorf("decoding files: %w", err)
	}
	return &FilePage{Items: nonNil(raw.Items), Metadata: raw.Metadata}, nil
}

// File fetches one file's metadata by id (GET /oa/files/{file_id}).
func (a *API) File(ctx context.Context, id string) (*File, error) {
	resp, err := a.c.GetOrganizationFileWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(resp.Body, &f); err != nil {
		return nil, fmt.Errorf("decoding file: %w", err)
	}
	return &f, nil
}

// FileUsage is one place an uploaded file is used, as Addigy tracks it.
type FileUsage struct {
	FileID             string `json:"file_id"`
	FeatureType        string `json:"feature_type"`
	FeatureName        string `json:"feature_name"`
	ItemID             string `json:"item_id"`
	ItemName           string `json:"item_name"`
	OSType             string `json:"os_type"`
	IsOnboardingConfig bool   `json:"is_onboarding_config"`
}

// FileUsages lists where the given files are used (POST /files/usage). A
// file that is used nowhere has no entries.
func (a *API) FileUsages(ctx context.Context, ids []string) ([]FileUsage, error) {
	resp, err := a.c.GetTrackedFilesWithResponse(ctx, gen.GetTrackedFilesJSONRequestBody{FileIds: &ids})
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode(), resp.Body); err != nil {
		return nil, err
	}
	var u []FileUsage
	if err := json.Unmarshal(resp.Body, &u); err != nil {
		return nil, fmt.Errorf("decoding file usages: %w", err)
	}
	return nonNil(u), nil
}
