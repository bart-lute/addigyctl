package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
	"github.com/ginkio/addigyctl/internal/policytree"
	"github.com/ginkio/addigyctl/internal/xlsx"
)

// ---- devices export --------------------------------------------------------

type DevicesExportCmd struct {
	File   string   `arg:"" optional:"" type:"path" help:"Excel file to write. Default: devices-<policy>-<date>.xlsx (devices-all-<date>.xlsx without --policy) in the current folder."`
	Policy string   `help:"Only devices located in this policy or any of its sub-policies. Accepts an ID, a name, or a 'Parent / Child' path."`
	Direct bool     `help:"With --policy: only devices located directly in that policy, not in its sub-policies."`
	Facts  []string `name:"fact" short:"f" help:"Only export these fact identifiers, in this order (comma-separated or repeated), and no static fields. Default: every fact and static field in your tenant."`
	Force  bool     `help:"Overwrite the file if it already exists."`
}

// exportSheet is the name of the worksheet.
const exportSheet = "Devices"

// exportLeadFacts are the first columns, in this order, followed by the
// device's policy. The remaining columns follow alphabetically.
var exportLeadFacts = []string{"device_name", "serial_number"}

// exportColumn is one column of the export: its header and how to get a
// device's cell value.
type exportColumn struct {
	header string
	value  func(addigy.Device) any
}

func (c *DevicesExportCmd) Run(app *App) error {
	if c.Direct && c.Policy == "" {
		return errors.New("--direct only makes sense together with --policy")
	}
	if app.G.JSON || app.G.Output != "" {
		return errors.New("devices export always writes an Excel file; --output and --json don't apply")
	}
	api, err := app.API()
	if err != nil {
		return err
	}
	org, err := app.OrgID()
	if err != nil {
		return err
	}
	loc, err := app.Location()
	if err != nil {
		return err
	}

	defs, err := api.AvailableFacts(app.Ctx, org)
	if err != nil {
		return err
	}
	facts, err := c.exportFacts(defs)
	if err != nil {
		return err
	}

	pols, err := api.QueryPolicies(app.Ctx, nil)
	if err != nil {
		return err
	}
	idx := policytree.New(pols)
	scope, member := "all", map[string]bool(nil)
	if c.Policy != "" {
		root, err := idx.Resolve(c.Policy)
		if err != nil {
			return err
		}
		member, scope = idx.Members(root.ID, c.Direct), root.Name
	}

	path := c.File
	if path == "" {
		path = xlsx.FileName("devices", scope, time.Now().In(loc))
	}
	if !c.Force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
		}
	}

	devices, reported, err := api.AllDevices(app.Ctx, addigy.DeviceQuery{
		Facts:     withFact(facts, addigy.LocationFact),
		SortField: addigy.StableSortField,
		PerPage:   fetchPageSize,
	})
	if err != nil {
		return err
	}
	if reported > 0 && len(devices) != reported {
		fmt.Fprintf(app.Err, "warning: Addigy reported %d devices but %d were received; the export may be incomplete\n", reported, len(devices))
	}
	if member != nil {
		devices = addigy.LocatedIn(devices, member)
	}
	sortExportRows(devices)

	var static []addigy.StaticField
	values := map[string]map[string]string{} // agent ID -> static field ID -> value
	if len(c.Facts) == 0 {
		if static, err = api.StaticFields(app.Ctx); err != nil {
			return fmt.Errorf("fetching static fields: %w", err)
		}
		vals, err := api.StaticFieldValues(app.Ctx)
		if err != nil {
			return fmt.Errorf("fetching static field values: %w", err)
		}
		for _, v := range vals {
			if values[v.AgentID] == nil {
				values[v.AgentID] = map[string]string{}
			}
			values[v.AgentID][v.FieldID] = v.Value
		}
	}

	cols := exportColumns(facts, defs, static, values, idx, loc, len(c.Facts) == 0)
	headers := make([]string, len(cols))
	for i, col := range cols {
		headers[i] = col.header
	}
	rows := make([][]any, len(devices))
	for r, d := range devices {
		rows[r] = make([]any, len(cols))
		for i, col := range cols {
			rows[r][i] = col.value(d)
		}
	}
	if err := xlsx.Write(path, headers, rows, xlsx.Options{Sheet: exportSheet, DateFormat: app.Cfg.DateFormat}); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "Exported %s (%s) to %s\n",
		count(len(devices), "device", "devices"), count(len(cols), "column", "columns"), path)
	return nil
}

// exportFacts returns the fact identifiers to fetch: --fact (each checked
// against the tenant's facts) or every fact the tenant has. The lead facts
// are always included.
func (c *DevicesExportCmd) exportFacts(defs []addigy.FactDef) ([]string, error) {
	if len(c.Facts) == 0 {
		ids := make([]string, 0, len(defs))
		for _, d := range defs {
			ids = append(ids, d.Identifier)
		}
		for _, id := range exportLeadFacts {
			ids = withFact(ids, id)
		}
		return ids, nil
	}
	known := make(map[string]bool, len(defs))
	for _, d := range defs {
		known[d.Identifier] = true
	}
	var unknown []string
	for _, id := range c.Facts {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown fact identifier(s): %s (see `addigyctl facts list`)", strings.Join(unknown, ", "))
	}
	ids := append([]string(nil), c.Facts...)
	for _, id := range exportLeadFacts {
		ids = withFact(ids, id)
	}
	return ids, nil
}

// exportColumns lays out the columns: the lead facts and the policy first,
// then the other facts and the static fields. With sorted set those are
// ordered by header (case-insensitively); otherwise the facts keep their
// given order.
func exportColumns(facts []string, defs []addigy.FactDef, static []addigy.StaticField,
	values map[string]map[string]string, idx *policytree.Index, loc *time.Location, sorted bool) []exportColumn {
	names := make(map[string]string, len(defs))
	for _, d := range defs {
		names[d.Identifier] = d.Name
	}
	factColumn := func(id string) exportColumn {
		header := names[id]
		if header == "" {
			header = id
		}
		return exportColumn{header, func(d addigy.Device) any { return exportFactValue(d, id, loc) }}
	}

	var cols []exportColumn
	for _, id := range exportLeadFacts {
		cols = append(cols, factColumn(id))
	}
	cols = append(cols, exportColumn{"Policy", func(d addigy.Device) any {
		v, _ := d.Value(addigy.LocationFact)
		id, _ := v.(string)
		if p, ok := idx.Get(id); ok {
			return idx.Path(p)
		}
		return id
	}})

	lead := map[string]bool{}
	for _, id := range exportLeadFacts {
		lead[id] = true
	}
	var rest []exportColumn
	for _, id := range facts {
		if !lead[id] {
			rest = append(rest, factColumn(id))
		}
	}
	for _, sf := range static {
		rest = append(rest, exportColumn{sf.Name, func(d addigy.Device) any {
			if v := values[d.AgentID][sf.ID]; v != "" {
				return v
			}
			return nil
		}})
	}
	if sorted {
		sort.SliceStable(rest, func(i, j int) bool {
			a, b := strings.ToLower(rest[i].header), strings.ToLower(rest[j].header)
			if a != b {
				return a < b
			}
			return rest[i].header < rest[j].header
		})
	}
	return append(cols, rest...)
}

// exportFactValue returns a device's fact as a cell value: a number for a
// number, a time.Time in loc for a timestamp, nil for a missing fact, and
// text for everything else. A list of plain values is joined with ", ".
func exportFactValue(d addigy.Device, id string, loc *time.Location) any {
	f, ok := d.Facts[id]
	if !ok || f.Value == nil {
		return nil
	}
	switch v := f.Value.(type) {
	case float64:
		return v
	case string:
		if f.Type == "date" || f.Type == "datetime" {
			if t := output.ParseTime(v); !t.IsZero() {
				return t.In(loc)
			}
		}
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, e := range v {
			switch e.(type) {
			case string, float64, bool, nil:
				parts = append(parts, output.Field(e))
			default:
				return output.Field(v) // nested values: keep the JSON
			}
		}
		return strings.Join(parts, ", ")
	}
	return output.Field(f.Value)
}

// sortExportRows orders devices by name, then serial number.
func sortExportRows(ds []addigy.Device) {
	key := func(d addigy.Device, id string) string {
		v, _ := d.Value(id)
		return strings.ToLower(output.Field(v))
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if a, b := key(ds[i], "device_name"), key(ds[j], "device_name"); a != b {
			return a < b
		}
		return key(ds[i], "serial_number") < key(ds[j], "serial_number")
	})
}
