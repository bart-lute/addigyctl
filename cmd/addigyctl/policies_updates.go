package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
	"github.com/ginkio/addigyctl/internal/policytree"
)

// ---- policies updates ------------------------------------------------------

type PoliciesUpdatesCmd struct {
	Policy   string   `help:"Only this policy and its sub-policies (ID, name or 'Parent / Child' path)."`
	Direct   bool     `help:"With --policy: only that policy, not its sub-policies."`
	OS       []string `name:"os" help:"Only these operating systems: macos, ios, ipados, tvos (comma-separated or repeated)."`
	Disabled bool     `help:"Also show operating systems whose updates are disabled, adding an ENABLED column."`
}

// osLabel is how each of addigy.UpdateOSes is shown.
var osLabel = map[string]string{"macos": "macOS", "ios": "iOS", "ipados": "iPadOS", "tvos": "tvOS"}

// updatesPermission is the API key permission the settings need.
const updatesPermission = "View System Updates Settings"

// updateColumn is one setting shown as a column: its header and the field
// of an OS's settings it shows. Table output shows the first tableColumns.
type updateColumn struct{ header, field string }

var updateColumns = []updateColumn{
	{"MODE", "update_mode"},
	{"MAX VERSION", "max_os_version_allowed"},
	{"DAYS AFTER RELEASE", "days_after_release"},
	{"DEFERRALS", "deferrals"},
	// The web app sets this together with update_mode ("latest" = true), so
	// table output leaves it out.
	{"KEEP UPDATED", "keep_os_updated"},
	// According to Apple this only applies to the "InstallLater" install
	// action, so table output leaves it out.
	{"USER DEFERRALS", "max_user_deferrals"},
	{"DELIVERY", "delivery_method"},
	{"HOURS AFTER RELEASE", "hours_after_release"},
	{"MINUTES AFTER RELEASE", "minutes_after_release"},
	{"INSTALL ACTION", "install_action"},
	{"ALLOWED DAYS", "allowed_days"},
	{"RESEND COMMAND HOUR", "resend_update_command_hour"},
	{"FORCE PAST DUE", "force_past_due"},
	{"NOTIFICATIONS", "notifications"},
	{"RECOMMENDED CADENCE", "recommended_cadence"},
	{"STANDARD USERS MAY UPDATE", "allow_standard_user_os_updates"},
	{"BETA UPDATES", "allow_beta_updates_in_ddm"},
	{"RAPID SECURITY RESPONSE", "rapid_security_response"},
}

const tableColumns = 4

func (c *PoliciesUpdatesCmd) Run(app *App) error {
	if c.Direct && c.Policy == "" {
		return errors.New("--direct only makes sense together with --policy")
	}
	oses, err := updateOSes(c.OS)
	if err != nil {
		return err
	}
	api, err := app.API()
	if err != nil {
		return err
	}
	pols, err := api.QueryPolicies(app.Ctx, nil)
	if err != nil {
		return err
	}
	idx := policytree.New(pols)

	scope := idx.All()
	suffix := ""
	if c.Policy != "" {
		root, err := idx.Resolve(c.Policy)
		if err != nil {
			return err
		}
		scope = []addigy.Policy{root}
		suffix = fmt.Sprintf(" in %q", root.Name)
		if !c.Direct {
			scope = idx.Subtree(root.ID)
			suffix += " and its sub-policies"
		}
	}
	// Parents before children, siblings by name: the order of the tree.
	paths := make(map[string]string, len(scope))
	for _, p := range scope {
		paths[p.ID] = idx.Path(p)
	}
	sort.SliceStable(scope, func(i, j int) bool {
		return strings.ToLower(paths[scope[i].ID]) < strings.ToLower(paths[scope[j].ID])
	})

	ids := make([]string, len(scope))
	for i, p := range scope {
		ids[i] = p.ID
	}
	settings, err := api.AllSystemUpdateSettings(app.Ctx, ids)
	var apiErr *addigy.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
		return fmt.Errorf("%w\nThe API key needs the permission %q.", err, updatesPermission)
	}
	if err != nil {
		return fmt.Errorf("fetching system update settings: %w", err)
	}

	type row struct {
		policy addigy.Policy
		os     string
		fields map[string]any
	}
	var rows []row
	withSettings := 0
	for i, s := range settings {
		found := false
		for _, os := range oses {
			f, ok := s.OS[os]
			if !ok || (!c.Disabled && f["enabled"] != true) {
				continue
			}
			rows = append(rows, row{scope[i], os, f})
			found = true
		}
		if found {
			withSettings++
		}
	}

	if app.json() {
		var raws []json.RawMessage
		for _, s := range settings {
			for _, r := range rows {
				if r.policy.ID == s.PolicyID {
					raws = append(raws, s.Raw)
					break
				}
			}
		}
		return output.JSON(app.Out, raws)
	}

	cols := updateColumns
	headers := []string{"POLICY", "OS"}
	if app.csv() {
		headers = []string{"POLICY ID", "POLICY", "PATH", "OS", "ENABLED"}
	} else {
		cols = cols[:tableColumns]
		if c.Disabled {
			headers = append(headers, "ENABLED")
		}
	}
	for _, col := range cols {
		headers = append(headers, col.header)
	}
	if app.csv() {
		headers = append(headers, "LAST UPDATED")
	}
	lastUpdated := map[string]string{}
	for _, s := range settings {
		lastUpdated[s.PolicyID] = s.LastUpdated
	}

	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		var line []string
		if app.csv() {
			line = []string{r.policy.ID, r.policy.Name, paths[r.policy.ID], osLabel[r.os], app.cell(r.fields["enabled"])}
		} else {
			line = []string{output.Truncate(r.policy.Name, 50), osLabel[r.os]}
			if c.Disabled {
				line = append(line, app.cell(r.fields["enabled"]))
			}
		}
		for _, col := range cols {
			v := settingValue(r.fields[col.field])
			// In "latest" mode the web app hides the maximum version and
			// doesn't apply it; only CSV and JSON show the stored value.
			if col.field == "max_os_version_allowed" && r.fields["update_mode"] == "latest" && !app.csv() {
				v = nil
			}
			line = append(line, app.cell(v))
		}
		if app.csv() {
			line = append(line, lastUpdated[r.policy.ID])
		}
		out = append(out, line)
	}
	if err := output.Rows(app.Out, app.Format(), headers, out, app.borders()); err != nil {
		return err
	}
	state := "enabled"
	if c.Disabled {
		state = "stored"
	}
	app.footer("%d of %s%s with %s update settings (%s)", withSettings, count(len(scope), "policy", "policies"), suffix, state, count(len(rows), "row", "rows"))
	if !app.csv() {
		fmt.Fprintln(app.Out, "These are each policy's own settings; settings a policy may get from a parent are not shown. -o csv or -j shows every field.")
	}
	return nil
}

// updateOSes validates --os, returning addigy.UpdateOSes when it is empty.
func updateOSes(names []string) ([]string, error) {
	if len(names) == 0 {
		return addigy.UpdateOSes, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if _, ok := osLabel[n]; !ok {
			return nil, fmt.Errorf("unknown --os %q: use macos, ios, ipados or tvos", n)
		}
		want[n] = true
	}
	var oses []string
	for _, os := range addigy.UpdateOSes {
		if want[os] {
			oses = append(oses, os)
		}
	}
	return oses, nil
}

// settingValue renders a nested setting compactly: the deferrals as
// "major 30, minor 7", leaving out periods of 0; lists joined with spaces;
// anything else as is.
func settingValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			if n, ok := x[k].(float64); ok && n == 0 {
				continue
			}
			parts = append(parts, strings.TrimSuffix(strings.TrimSuffix(k, "_in_days"), "_period")+" "+output.Field(x[k]))
		}
		return strings.Join(parts, ", ")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = output.Field(e)
		}
		return strings.Join(parts, " ")
	}
	return v
}
