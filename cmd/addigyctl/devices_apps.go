package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

// ---- devices apps ----------------------------------------------------------

type DevicesAppsCmd struct {
	Policy string `help:"Only devices located in this policy or any of its sub-policies (ID, name or 'Parent / Child' path). Default: every device."`
	Direct bool   `help:"With --policy: only devices located directly in that policy, not in its sub-policies."`
	System bool   `help:"Also list Apple's built-in apps (found under /System)."`
	Sort   string `default:"name" help:"Column to sort by: name (default, A-Z) or devices (most first)."`
	Desc   bool   `help:"Reverse the column's default order."`
}

// systemAppsPrefix is where macOS keeps its built-in apps.
const systemAppsPrefix = "/System/"

// appSummary is one application across devices.
type appSummary struct {
	Name     string           `json:"name"`
	Devices  int              `json:"devices"`
	Versions []versionSummary `json:"versions"`
	Paths    []string         `json:"paths"`
}

type versionSummary struct {
	Version string `json:"version"`
	Devices int    `json:"devices"`
}

func (c *DevicesAppsCmd) Run(app *App) error {
	if c.Direct && c.Policy == "" {
		return errors.New("--direct only makes sense together with --policy")
	}
	if c.Sort != "name" && c.Sort != "devices" {
		return fmt.Errorf("unknown --sort %q: use name or devices", c.Sort)
	}
	api, err := app.API()
	if err != nil {
		return err
	}

	all, reported, err := api.AllDevices(app.Ctx, addigy.DeviceQuery{
		Facts:     []string{addigy.LocationFact},
		SortField: addigy.StableSortField,
		PerPage:   fetchPageSize,
	})
	if err != nil {
		return err
	}
	if reported > 0 && len(all) != reported {
		fmt.Fprintf(app.Err, "warning: Addigy reported %d devices but %d were received; the inventory may be incomplete\n", reported, len(all))
	}
	devices, scope := all, ""
	if c.Policy != "" {
		root, idx, err := resolvePolicy(app, api, c.Policy)
		if err != nil {
			return err
		}
		member := idx.Members(root.ID, c.Direct)
		devices = addigy.LocatedIn(all, member)
		scope = fmt.Sprintf(" in %q", root.Name)
		if n := len(member) - 1; n > 0 {
			scope += " and " + count(n, "sub-policy", "sub-policies")
		}
	}

	ids := make([]string, len(devices))
	for i, d := range devices {
		ids[i] = d.AgentID
	}
	apps, total, err := api.AllInstalledApps(app.Ctx, ids)
	if err != nil {
		return fmt.Errorf("fetching installed applications: %w", err)
	}
	if len(apps) < total {
		fmt.Fprintf(app.Err, "warning: Addigy reported %d applications but %d were received; the inventory may be incomplete\n", total, len(apps))
	}

	summaries, reporting := summarizeApps(apps, c.System)
	sortApps(summaries, c.Sort, c.Desc)

	if app.json() {
		return output.JSON(app.Out, summaries)
	}
	headers := []string{"APP", "DEVICES", "VERSIONS"}
	rows := make([][]string, 0, len(summaries))
	for _, s := range summaries {
		parts := make([]string, len(s.Versions))
		for i, v := range s.Versions {
			parts[i] = fmt.Sprintf("%s (%d)", output.Value(v.Version), v.Devices)
		}
		versions := strings.Join(parts, ", ")
		name := s.Name
		if !app.csv() {
			name, versions = output.Truncate(name, 50), output.Truncate(versions, 80)
		}
		rows = append(rows, []string{name, strconv.Itoa(s.Devices), versions})
	}
	if err := output.Rows(app.Out, app.Format(), headers, rows, app.borders()); err != nil {
		return err
	}
	app.footer("%s on %d of %s%s", count(len(summaries), "app", "apps"), reporting, count(len(devices), "device", "devices"), scope)
	if !app.csv() {
		fmt.Fprintln(app.Out, "Devices count those that reported applications through the Addigy agent (Macs only).")
		if !c.System {
			fmt.Fprintln(app.Out, "Apple's built-in apps (under /System) are left out; --system includes them.")
		}
	}
	return nil
}

// summarizeApps groups applications by name across devices, leaving out
// Apple's built-in apps unless system is set. It also returns how many
// devices reported any application at all.
func summarizeApps(apps []addigy.InstalledApp, system bool) ([]appSummary, int) {
	type group struct {
		devices  map[string]bool
		versions map[string]map[string]bool // version → devices
		paths    map[string]bool
	}
	groups := map[string]*group{}
	reporting := map[string]bool{}
	for _, a := range apps {
		reporting[a.AgentID] = true
		if !system && strings.HasPrefix(a.Path, systemAppsPrefix) {
			continue
		}
		g := groups[a.Name]
		if g == nil {
			g = &group{map[string]bool{}, map[string]map[string]bool{}, map[string]bool{}}
			groups[a.Name] = g
		}
		g.devices[a.AgentID] = true
		if g.versions[a.Version] == nil {
			g.versions[a.Version] = map[string]bool{}
		}
		g.versions[a.Version][a.AgentID] = true
		g.paths[a.Path] = true
	}

	out := make([]appSummary, 0, len(groups))
	for name, g := range groups {
		s := appSummary{Name: name, Devices: len(g.devices)}
		for v, devs := range g.versions {
			s.Versions = append(s.Versions, versionSummary{v, len(devs)})
		}
		sort.Slice(s.Versions, func(i, j int) bool { return compareVersions(s.Versions[i].Version, s.Versions[j].Version) > 0 })
		for p := range g.paths {
			s.Paths = append(s.Paths, p)
		}
		sort.Strings(s.Paths)
		out = append(out, s)
	}
	return out, len(reporting)
}

// sortApps orders by name (A-Z) or by devices (most first); desc reverses
// that. Ties go by name.
func sortApps(apps []appSummary, by string, desc bool) {
	sort.Slice(apps, func(i, j int) bool {
		a, b := apps[i], apps[j]
		if by == "devices" && a.Devices != b.Devices {
			return (a.Devices > b.Devices) != desc
		}
		c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		if c == 0 {
			c = strings.Compare(a.Name, b.Name)
		}
		if by == "name" && desc {
			return c > 0
		}
		return c < 0
	})
}

// compareVersions compares dotted versions part by part, numerically where
// both parts are numbers ("10.2" < "10.10"), else as text.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, errA := strconv.Atoi(pa[i])
		nb, errB := strconv.Atoi(pb[i])
		if errA == nil && errB == nil {
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
			continue
		}
		if c := strings.Compare(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	return len(pa) - len(pb)
}
