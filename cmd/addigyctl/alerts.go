package main

import (
	"fmt"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

type AlertsCmd struct {
	List AlertsListCmd `cmd:"" help:"List received alerts."`
}

// Addigy's actual status values for a received alert. Kept as-is rather than
// translated into the web GUI's tab names, which are confusing (its "Open"
// tab shows the same alerts as "Unattended").
const (
	statusUnattended   = "Unattended"
	statusAcknowledged = "Acknowledged"
	statusResolved     = "Resolved"
)

// alertFetchPageSize is Addigy's maximum per_page for the alerts endpoint,
// used when a filter (--muted) forces fetching every matching page.
const alertFetchPageSize = 100

type AlertsListCmd struct {
	Unattended   bool   `help:"Only unattended alerts."`
	Acknowledged bool   `help:"Only acknowledged alerts."`
	Resolved     bool   `help:"Only resolved alerts."`
	All          bool   `help:"Every status (default: unattended and acknowledged, i.e. not yet resolved)."`
	Muted        bool   `help:"Only muted alerts. Addigy has no server-side filter for this or --known-devices, so either forces fetching every page matching the status filter and filtering locally, which can be slow combined with --all."`
	KnownDevices bool   `name:"known-devices" help:"Only alerts for a device that still exists (matches the web GUI, which hides alerts for removed devices). Addigy keeps alert history for devices long after they're gone, e.g. \"Missing for 30 days\" alerts that outlive the device itself."`
	Category     string `help:"Only alerts in this category."`
	NameContains string `name:"name-contains" help:"Only alerts whose name contains this text."`
	Sort         string `help:"Column to sort by: created (default, oldest first), name, level, status or category."`
	Desc         bool   `help:"Sort descending."`
	Page         int    `default:"1" help:"Page number."`
	PerPage      int    `name:"per-page" default:"50" help:"Alerts per page."`
}

// statuses returns the status values to filter on: the ones explicitly
// selected, "Unattended" and "Acknowledged" (i.e. not yet resolved) if none
// were, or every status with --all.
func (c *AlertsListCmd) statuses() []string {
	if c.All {
		return nil
	}
	var sel []string
	if c.Unattended {
		sel = append(sel, statusUnattended)
	}
	if c.Acknowledged {
		sel = append(sel, statusAcknowledged)
	}
	if c.Resolved {
		sel = append(sel, statusResolved)
	}
	if len(sel) == 0 {
		return []string{statusUnattended, statusAcknowledged}
	}
	return sel
}

// alertSortFields maps a --sort column to Addigy's sort_field name.
var alertSortFields = map[string]string{
	"created":  "created_date",
	"name":     "name",
	"level":    "level",
	"status":   "status",
	"category": "category",
}

func (c *AlertsListCmd) Run(app *App) error {
	col := strings.ToLower(c.Sort)
	if col == "" {
		col = "created"
	}
	sortField, ok := alertSortFields[col]
	if !ok {
		return fmt.Errorf("unknown --sort column %q (use one of: created, name, level, status, category)", col)
	}

	api, err := app.API()
	if err != nil {
		return err
	}

	q := addigy.AlertQuery{
		Statuses:     c.statuses(),
		Category:     c.Category,
		NameContains: c.NameContains,
		SortField:    sortField,
		Desc:         c.Desc,
		Page:         c.Page,
		PerPage:      c.PerPage,
	}

	// The device index is needed to filter by --known-devices, and to show
	// SERIAL NUMBER/DEVICE NAME in every non-JSON format; skip it for plain
	// --json, which prints alerts as Addigy returns them.
	var devices map[string]deviceIdentity
	if c.KnownDevices || !app.json() {
		devices, err = alertDeviceIndex(app, api)
		if err != nil {
			return err
		}
	}

	var (
		alerts []addigy.Alert
		meta   addigy.PageMetadata
	)
	if c.Muted || c.KnownDevices {
		alerts, meta, err = c.fetchFiltered(app, api, q, devices)
	} else {
		var pg *addigy.AlertPage
		pg, err = api.SearchAlerts(app.Ctx, q)
		if err == nil {
			alerts, meta = pg.Items, pg.Metadata
		}
	}
	if err != nil {
		return err
	}

	if app.json() {
		return output.JSON(app.Out, alerts)
	}

	style, err := app.DateStyle()
	if err != nil {
		return err
	}

	rows := make([][]string, 0, len(alerts))
	for _, al := range alerts {
		dev := devices[al.AgentID]
		rows = append(rows, []string{
			app.cell(al.Level),
			al.Name,
			app.cell(al.Category),
			app.cell(dev.serial),
			app.cell(dev.name),
			al.Status,
			app.cell(al.Muted),
			app.cell(style.DateTime(al.CreatedDate)),
		})
	}
	headers := []string{"LEVEL", "NAME", "CATEGORY", "SERIAL NUMBER", "DEVICE NAME", "STATUS", "MUTED", "CREATED"}
	if err := output.Rows(app.Out, app.Format(), headers, rows, app.borders()); err != nil {
		return err
	}
	app.footer("%d of %d alerts", len(alerts), meta.Total)
	return nil
}

// fetchFiltered fetches every page matching q's status/category/name filter,
// applies --muted and/or --known-devices (Addigy has no server-side filter
// for either), and paginates the result the same way the server-side path
// would.
func (c *AlertsListCmd) fetchFiltered(app *App, api *addigy.API, q addigy.AlertQuery, devices map[string]deviceIdentity) ([]addigy.Alert, addigy.PageMetadata, error) {
	all, err := fetchAllAlerts(app, api, q)
	if err != nil {
		return nil, addigy.PageMetadata{}, err
	}
	kept := all[:0:0]
	for _, al := range all {
		if c.Muted && !al.Muted {
			continue
		}
		if c.KnownDevices {
			if _, ok := devices[al.AgentID]; !ok {
				continue
			}
		}
		kept = append(kept, al)
	}
	shown, meta := paginateAlerts(kept, c.Page, c.PerPage)
	return shown, meta, nil
}

// fetchAllAlerts returns every alert matching q, fetching pages sequentially
// at Addigy's maximum page size. Each page is already sorted per q, so the
// concatenated result stays in that order.
func fetchAllAlerts(app *App, api *addigy.API, q addigy.AlertQuery) ([]addigy.Alert, error) {
	q.Page = 1
	q.PerPage = alertFetchPageSize
	first, err := api.SearchAlerts(app.Ctx, q)
	if err != nil {
		return nil, err
	}
	all := append([]addigy.Alert(nil), first.Items...)
	for p := 2; p <= first.Metadata.PageCount; p++ {
		q.Page = p
		pg, err := api.SearchAlerts(app.Ctx, q)
		if err != nil {
			return nil, err
		}
		all = append(all, pg.Items...)
	}
	return all, nil
}

// paginateAlerts slices an already complete, already sorted result set.
func paginateAlerts(alerts []addigy.Alert, page, perPage int) ([]addigy.Alert, addigy.PageMetadata) {
	total := len(alerts)
	if perPage <= 0 {
		perPage = total
	}
	if page < 1 {
		page = 1
	}
	pageCount := 1
	if perPage > 0 {
		pageCount = (total + perPage - 1) / perPage
	}
	if pageCount == 0 {
		pageCount = 1
	}
	start := min(total, (page-1)*perPage)
	end := min(total, start+perPage)
	shown := alerts[start:end]
	return shown, addigy.PageMetadata{
		Page: page, PageCount: pageCount, PerPage: perPage, ResultCount: len(shown), Total: total,
	}
}

// deviceIdentity is a device's human-readable identity, looked up by agent
// ID for alert rows (which only carry the agent ID).
type deviceIdentity struct {
	serial, name string
}

// alertDeviceIndex maps every device's agent ID to its serial number and
// name.
func alertDeviceIndex(app *App, api *addigy.API) (map[string]deviceIdentity, error) {
	all, reported, err := fetchAllDevices(app.Ctx, api, addigy.DeviceQuery{
		Facts:     []string{"serial_number", "device_name"},
		SortField: stableSortField,
		PerPage:   fetchPageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("resolving alert devices: %w", err)
	}
	if reported > 0 && len(all) != reported {
		fmt.Fprintf(app.Err, "warning: Addigy reported %d devices but %d were received; some alerts may be missing a serial number or device name\n", reported, len(all))
	}
	idx := make(map[string]deviceIdentity, len(all))
	for _, d := range all {
		serial, _ := factValue(d, "serial_number")
		name, _ := factValue(d, "device_name")
		s, _ := serial.(string)
		n, _ := name.(string)
		idx[d.AgentID] = deviceIdentity{serial: s, name: n}
	}
	return idx, nil
}
