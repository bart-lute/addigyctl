package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

// defaultDeviceFacts are the table columns used when neither --fact nor the
// config file's "device_facts" is set. Run `addigyctl facts list` to see every
// identifier available in your tenant.
var defaultDeviceFacts = []string{"serial_number", "device_name", "os_version"}

type DevicesCmd struct {
	List     DevicesListCmd     `cmd:"" help:"Search devices (Universal Search)."`
	Get      DevicesGetCmd      `cmd:"" help:"Show one device with all of its facts."`
	Policies DevicesPoliciesCmd `cmd:"" help:"Show the policies assigned to a device."`
}

// ---- devices list ----------------------------------------------------------

type DevicesListCmd struct {
	Search  string   `arg:"" optional:"" help:"Free-text search across device facts."`
	Policy  string   `help:"Only devices located in this policy or any of its sub-policies (a device's location is its policy_id fact). Accepts an ID, a name, or a 'Parent / Child' path."`
	Direct  bool     `help:"With --policy: only devices located directly in that policy, not in its sub-policies."`
	Facts   []string `name:"fact" short:"f" help:"Fact identifiers to show as columns (comma-separated or repeated). Default: config device_facts, else serial_number,device_name,os_version."`
	Sort    string   `help:"Fact identifier to sort by. With --policy the default is the first --fact column."`
	Desc    bool     `help:"Sort descending."`
	Page    int      `default:"1" help:"Page number."`
	PerPage int      `name:"per-page" default:"50" help:"Devices per page."`
	All     bool     `help:"Fetch all pages."`
}

func (c *DevicesListCmd) Run(app *App) error {
	if c.Direct && c.Policy == "" {
		return errors.New("--direct only makes sense together with --policy")
	}
	api, err := app.API()
	if err != nil {
		return err
	}
	facts := firstNonEmpty(c.Facts, app.Cfg.DeviceFacts, defaultDeviceFacts)

	var (
		devices []addigy.Device
		meta    addigy.PageMetadata
		scope   string
		locate  func(addigy.Device) string // optional LOCATION column
	)
	if c.Policy == "" {
		devices, meta, err = c.searchServerSide(app, api, facts)
	} else {
		devices, meta, scope, locate, err = c.searchPolicy(app, api, facts)
	}
	if err != nil {
		return err
	}

	if app.json() {
		raws := make([]json.RawMessage, len(devices))
		for i, d := range devices {
			raws[i] = d.Raw
		}
		return output.JSON(app.Out, struct {
			Items    []json.RawMessage   `json:"items"`
			Metadata addigy.PageMetadata `json:"metadata"`
		}{raws, meta})
	}

	headers := []string{"AGENT ID"}
	for _, f := range facts {
		headers = append(headers, strings.ToUpper(strings.ReplaceAll(f, "_", " ")))
	}
	if locate != nil {
		headers = append(headers, "LOCATION")
	}
	rows := make([][]string, 0, len(devices))
	for _, d := range devices {
		row := []string{d.AgentID}
		for _, f := range facts {
			row = append(row, factCell(d, f, app.csv()))
		}
		if locate != nil {
			row = append(row, locate(d))
		}
		rows = append(rows, row)
	}
	if err := output.Rows(app.Out, app.Format(), headers, rows, app.borders()); err != nil {
		return err
	}
	total := meta.Total
	if total == 0 {
		total = len(devices)
	}
	app.footer("%d of %d devices%s", len(devices), total, scope)
	return nil
}

// searchServerSide runs a plain Universal Search, letting Addigy sort and
// paginate.
func (c *DevicesListCmd) searchServerSide(app *App, api *addigy.API, facts []string) ([]addigy.Device, addigy.PageMetadata, error) {
	q := addigy.DeviceQuery{
		Search: c.Search, Facts: facts,
		SortField: c.Sort, Desc: c.Desc, Page: c.Page, PerPage: c.PerPage,
	}
	var (
		devices []addigy.Device
		meta    addigy.PageMetadata
	)
	for {
		pg, err := api.SearchDevices(app.Ctx, q)
		if err != nil {
			return nil, meta, err
		}
		devices = append(devices, pg.Items...)
		meta = pg.Metadata
		if !c.All || len(pg.Items) == 0 || q.Page >= pg.Metadata.PageCount {
			break
		}
		q.Page++
	}
	return devices, meta, nil
}

// searchPolicy lists the devices whose location (the policy_id fact: every
// device has exactly one) is the given policy or, unless --direct is set, any
// of its sub-policies. Addigy can't filter on a policy subtree, so this
// fetches the devices (a few pages in parallel), filters on the location here,
// then sorts and paginates the result.
func (c *DevicesListCmd) searchPolicy(app *App, api *addigy.API, facts []string) ([]addigy.Device, addigy.PageMetadata, string, func(addigy.Device) string, error) {
	var none addigy.PageMetadata
	root, idx, err := resolvePolicy(app, api, c.Policy)
	if err != nil {
		return nil, none, "", nil, err
	}

	members := []addigy.Policy{root}
	if !c.Direct {
		members = idx.subtree(root.ID)
	}
	member := make(map[string]bool, len(members))
	for _, p := range members {
		member[p.ID] = true
	}

	sortField := c.Sort
	if sortField == "" {
		sortField = facts[0]
	}
	q := addigy.DeviceQuery{
		Search: c.Search,
		// The location and the sort field must be returned to filter and sort on them.
		Facts: withFact(withFact(facts, sortField), locationFact),
		// A fixed, (near-)unique server-side order keeps pages consistent while
		// they are fetched in parallel; the requested sort is applied below.
		SortField: stableSortField,
		PerPage:   fetchPageSize,
	}
	all, reported, err := fetchAllDevices(app.Ctx, api, q)
	if err != nil {
		return nil, none, "", nil, err
	}
	if reported > 0 && len(all) != reported {
		fmt.Fprintf(app.Err, "warning: Addigy reported %d devices but %d were received; the list may be incomplete\n", reported, len(all))
	}

	located := locatedIn(all, member)
	sortDevices(located, sortField, c.Desc)
	shown, meta := paginate(located, c.Page, c.PerPage, c.All)

	scope := fmt.Sprintf(" in %q", root.Name)
	var locate func(addigy.Device) string
	if n := len(members) - 1; n > 0 {
		scope += " and " + count(n, "sub-policy", "sub-policies")
		locate = func(d addigy.Device) string {
			v, _ := factValue(d, locationFact)
			id, _ := v.(string)
			if p, ok := idx.byID[id]; ok {
				return idx.path(p)
			}
			return id
		}
	}
	return shown, meta, scope, locate, nil
}

// factCell renders one fact of a device. Tables show "-" for missing facts and
// shorten long values; CSV fields are left empty and never shortened.
func factCell(d addigy.Device, id string, csv bool) string {
	f, ok := d.Facts[id]
	if csv {
		if !ok {
			return ""
		}
		return output.Field(f.Value)
	}
	if !ok {
		return "-"
	}
	if f.Value == nil && f.ErrorMsg != "" {
		return "(error)"
	}
	return output.Truncate(output.Value(f.Value), 60)
}

// ---- devices get -----------------------------------------------------------

type DevicesGetCmd struct {
	Ref   string   `arg:"" help:"Agent ID, serial number or device name."`
	Facts []string `name:"fact" short:"f" help:"Only fetch these fact identifiers (default: whatever the API returns)."`
}

// getSearchSize is how many search results devices get looks at.
const getSearchSize = 25

func (c *DevicesGetCmd) Run(app *App) error {
	api, err := app.API()
	if err != nil {
		return err
	}
	pg, err := api.SearchDevices(app.Ctx, addigy.DeviceQuery{Search: c.Ref, Facts: c.Facts, Page: 1, PerPage: getSearchSize})
	if err != nil {
		return err
	}
	dev, err := pickDevice(pg.Items, c.Ref, pg.Metadata.Total)
	if err != nil {
		if hint := policyHint(app, api, c.Ref); hint != "" {
			err = fmt.Errorf("%w\n%s", err, hint)
		}
		return err
	}

	if app.json() {
		return output.JSON(app.Out, dev.Raw)
	}

	if !app.csv() {
		fmt.Fprintf(app.Out, "Agent ID:    %s\n", dev.AgentID)
		fmt.Fprintf(app.Out, "Org ID:      %s\n", output.Value(dev.OrgID))
		fmt.Fprintf(app.Out, "Audit date:  %s\n\n", output.Value(dev.AuditDate))
	}

	names := make([]string, 0, len(dev.Facts))
	for n := range dev.Facts {
		names = append(names, n)
	}
	sort.Strings(names)
	rows := make([][]string, 0, len(names))
	for _, n := range names {
		f := dev.Facts[n]
		value := output.Field(f.Value)
		if !app.csv() {
			value = output.Truncate(output.Value(f.Value), 100)
		}
		rows = append(rows, []string{n, f.Type, value})
	}
	return output.Rows(app.Out, app.Format(), []string{"FACT", "TYPE", "VALUE"}, rows, app.borders())
}

// policyHint explains the mistake of passing a policy to a device command. It
// only runs after a lookup has already failed, so the extra request is cheap.
func policyHint(app *App, api *addigy.API, ref string) string {
	pols, err := api.QueryPolicies(app.Ctx, nil)
	if err != nil {
		return ""
	}
	p, err := newPolicyIndex(pols).resolve(ref)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%q is a policy (%s), not a device. To list its devices: addigyctl devices list --policy %s",
		p.Name, p.ID, p.ID)
}

// deviceIdentityFacts are the facts a device can be looked up by, besides its
// agent ID. Matching any fact value would make e.g. a policy ID (the value of
// every member's policy_id fact) match dozens of devices.
var deviceIdentityFacts = []string{"serial_number", "device_name"}

func identifiesDevice(d addigy.Device, ref string) bool {
	if strings.EqualFold(d.AgentID, ref) {
		return true
	}
	for _, id := range deviceIdentityFacts {
		if v, ok := factValue(d, id); ok {
			if s, ok := v.(string); ok && strings.EqualFold(s, ref) {
				return true
			}
		}
	}
	return false
}

// maxCandidates limits how many devices an ambiguity error lists.
const maxCandidates = 10

// pickDevice selects the device identified by ref: its agent ID, serial number
// or name. If nothing matches exactly but the search returned a single device,
// that device is used. total is the number of search results Addigy reported
// (items may hold only the first page of them).
func pickDevice(items []addigy.Device, ref string, total int) (addigy.Device, error) {
	var exact []addigy.Device
	for _, d := range items {
		if identifiesDevice(d, ref) {
			exact = append(exact, d)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) == 0 && len(items) == 1:
		return items[0], nil
	case len(items) == 0:
		return addigy.Device{}, fmt.Errorf("no device matches %q", ref)
	}

	candidates, what := exact, "identifies"
	if len(candidates) == 0 {
		candidates, what = items, "matches (but is not the agent ID, serial number or name of)"
	}
	n := max(total, len(candidates))

	var b strings.Builder
	fmt.Fprintf(&b, "%q %s %d devices; use the agent ID or serial number instead. First %d:", ref, what, n, min(len(candidates), maxCandidates))
	for _, d := range candidates[:min(len(candidates), maxCandidates)] {
		serial, _ := factValue(d, "serial_number")
		name, _ := factValue(d, "device_name")
		fmt.Fprintf(&b, "\n  %s  %s  %s", d.AgentID, output.Value(serial), output.Value(name))
	}
	if n > maxCandidates {
		fmt.Fprintf(&b, "\n  … and %d more", n-maxCandidates)
	}
	return addigy.Device{}, errors.New(b.String())
}

// ---- devices policies ------------------------------------------------------

type DevicesPoliciesCmd struct {
	AgentID string `arg:"" name:"agent-id" help:"Agent ID of the device."`
}

func (c *DevicesPoliciesCmd) Run(app *App) error {
	api, err := app.API()
	if err != nil {
		return err
	}
	org, err := app.OrgID()
	if err != nil {
		return err
	}
	ids, err := api.DevicePolicyAssignments(app.Ctx, org, c.AgentID)
	if err != nil {
		return err
	}
	if app.json() {
		return output.JSON(app.Out, ids)
	}

	names := map[string]string{}
	if len(ids) > 0 {
		pols, err := api.QueryPolicies(app.Ctx, ids)
		if err != nil {
			return err
		}
		names = policyNames(pols)
	}
	rows := make([][]string, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, []string{id, app.cell(names[id])})
	}
	return output.Rows(app.Out, app.Format(), []string{"POLICY ID", "NAME"}, rows, app.borders())
}

func firstNonEmpty(lists ...[]string) []string {
	for _, l := range lists {
		if len(l) > 0 {
			return l
		}
	}
	return nil
}
