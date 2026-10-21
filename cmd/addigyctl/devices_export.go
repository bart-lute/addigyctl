package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

// ---- devices export --------------------------------------------------------

type DevicesExportCmd struct {
	File   string   `arg:"" optional:"" type:"path" help:"Excel file to write. Default: devices-<policy>-<date>.xlsx (devices-all-<date>.xlsx without --policy) in the current folder."`
	Policy string   `help:"Only devices located in this policy or any of its sub-policies. Accepts an ID, a name, or a 'Parent / Child' path."`
	Direct bool     `help:"With --policy: only devices located directly in that policy, not in its sub-policies."`
	Facts  []string `name:"fact" short:"f" help:"Only export these fact identifiers, in this order (comma-separated or repeated), and no static fields. Default: every fact and static field in your tenant."`
	Force  bool     `help:"Overwrite the file if it already exists."`
}

const (
	exportSheet = "Devices"

	// exportHeaderFill is the background of the header row (a light blue).
	exportHeaderFill = "DDEBF7"

	// exportMinWidth and exportMaxWidth bound the column widths, in characters.
	exportMinWidth = 8
	exportMaxWidth = 50

	// excelDateTimeFormat is Excel's built-in date-and-time format (number
	// format 22). Excel shows it in the viewer's own regional format.
	excelDateTimeFormat = 22
)

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
	idx := newPolicyIndex(pols)
	scope, member := "all", map[string]bool(nil)
	if c.Policy != "" {
		root, err := idx.resolve(c.Policy)
		if err != nil {
			return err
		}
		members := []addigy.Policy{root}
		if !c.Direct {
			members = idx.subtree(root.ID)
		}
		member = make(map[string]bool, len(members))
		for _, p := range members {
			member[p.ID] = true
		}
		scope = root.Name
	}

	path := c.File
	if path == "" {
		path = fmt.Sprintf("devices-%s-%s.xlsx", fileSlug(scope), time.Now().In(loc).Format("2006-01-02"))
	}
	if !c.Force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
		}
	}

	devices, reported, err := fetchAllDevices(app.Ctx, api, addigy.DeviceQuery{
		Facts:     withFact(facts, locationFact),
		SortField: stableSortField,
		PerPage:   fetchPageSize,
	})
	if err != nil {
		return err
	}
	if reported > 0 && len(devices) != reported {
		fmt.Fprintf(app.Err, "warning: Addigy reported %d devices but %d were received; the export may be incomplete\n", reported, len(devices))
	}
	if member != nil {
		devices = locatedIn(devices, member)
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
	f, err := buildWorkbook(cols, devices, app.Cfg.DateFormat)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeWorkbook(f, path); err != nil {
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
	values map[string]map[string]string, idx *policyIndex, loc *time.Location, sorted bool) []exportColumn {
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
		v, _ := factValue(d, locationFact)
		id, _ := v.(string)
		if p, ok := idx.byID[id]; ok {
			return idx.path(p)
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
// number, a time.Time for a timestamp (in loc, as Excel has no time zones),
// nil for a missing fact, and text for everything else. A list of plain
// values is joined with ", ".
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
				t = t.In(loc)
				return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
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
		v, _ := factValue(d, id)
		return strings.ToLower(output.Field(v))
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if a, b := key(ds[i], "device_name"), key(ds[j], "device_name"); a != b {
			return a < b
		}
		return key(ds[i], "serial_number") < key(ds[j], "serial_number")
	})
}

// buildWorkbook writes the header and one row per device to a new workbook.
// The header row is bold on a light blue background, frozen and filterable.
// Timestamps use dateFormat (the config file's "date_format", whose tokens
// Excel understands too) or else Excel's regional date and time format.
func buildWorkbook(cols []exportColumn, devices []addigy.Device, dateFormat string) (*excelize.File, error) {
	f := excelize.NewFile()
	if err := f.SetSheetName("Sheet1", exportSheet); err != nil {
		return nil, err
	}
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{exportHeaderFill}},
	})
	if err != nil {
		return nil, err
	}
	dateStyle := &excelize.Style{NumFmt: excelDateTimeFormat}
	if dateFormat != "" {
		dateStyle = &excelize.Style{CustomNumFmt: &dateFormat}
	}
	dateStyleID, err := f.NewStyle(dateStyle)
	if err != nil {
		return nil, err
	}

	widths := make([]int, len(cols))
	isDate := make([]bool, len(cols))
	header := make([]any, len(cols))
	for i, c := range cols {
		header[i] = c.header
		widths[i] = utf8.RuneCountInString(c.header)
	}
	if err := f.SetSheetRow(exportSheet, "A1", &header); err != nil {
		return nil, err
	}
	row := make([]any, len(cols))
	for r, d := range devices {
		for i, c := range cols {
			v := c.value(d)
			switch x := v.(type) {
			case string:
				v = truncateCell(x)
				widths[i] = max(widths[i], utf8.RuneCountInString(x))
			case float64:
				widths[i] = max(widths[i], len(output.Field(x)))
			case time.Time:
				isDate[i] = true
				widths[i] = max(widths[i], 19)
			}
			row[i] = v
		}
		cell, err := excelize.CoordinatesToCellName(1, r+2)
		if err != nil {
			return nil, err
		}
		if err := f.SetSheetRow(exportSheet, cell, &row); err != nil {
			return nil, err
		}
	}

	lastRow := len(devices) + 1
	for i := range cols {
		name, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return nil, err
		}
		width := float64(min(max(widths[i], exportMinWidth), exportMaxWidth) + 2)
		if err := f.SetColWidth(exportSheet, name, name, width); err != nil {
			return nil, err
		}
		if isDate[i] {
			if err := f.SetCellStyle(exportSheet, name+"2", fmt.Sprintf("%s%d", name, lastRow), dateStyleID); err != nil {
				return nil, err
			}
		}
	}
	last, err := excelize.CoordinatesToCellName(len(cols), 1)
	if err != nil {
		return nil, err
	}
	if err := f.SetCellStyle(exportSheet, "A1", last, headerStyle); err != nil {
		return nil, err
	}
	if err := f.SetPanes(exportSheet, &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	}); err != nil {
		return nil, err
	}
	lastCell, err := excelize.CoordinatesToCellName(len(cols), lastRow)
	if err != nil {
		return nil, err
	}
	if err := f.AutoFilter(exportSheet, "A1:"+lastCell, nil); err != nil {
		return nil, err
	}
	return f, nil
}

// truncateCell shortens text to what an Excel cell can hold.
func truncateCell(s string) string {
	if utf8.RuneCountInString(s) <= excelize.TotalCellChars {
		return s
	}
	return output.Truncate(s, excelize.TotalCellChars)
}

// writeWorkbook saves the workbook readable by the current user only: an
// export can hold personal data such as user names and email addresses.
func writeWorkbook(f *excelize.File, path string) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Write(out); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// fileSlug turns a policy name into something safe for a file name.
func fileSlug(s string) string {
	if slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-"); slug != "" {
		return slug
	}
	return "policy"
}
