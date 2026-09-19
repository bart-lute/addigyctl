package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

type PoliciesCmd struct {
	List PoliciesListCmd `cmd:"" help:"List policies (root policies by default)."`
	Tree PoliciesTreeCmd `cmd:"" help:"Show the policy hierarchy as a tree."`
	Get  PoliciesGetCmd  `cmd:"" help:"Show one policy in full (JSON)."`
}

// ---- policies list ---------------------------------------------------------

type PoliciesListCmd struct {
	Parent   string   `help:"List the direct sub-policies of this policy (ID or name)."`
	All      bool     `help:"List policies at every level (flat) instead of only root policies."`
	IDs      []string `name:"id" help:"Only these policy IDs (comma-separated or repeated); searches all levels."`
	Name     string   `help:"Only policies whose name contains this text (case-insensitive); searches all levels unless --parent is set."`
	NoCounts bool     `name:"no-counts" help:"Skip the DEVICES column (saves fetching every device)."`
	Sort     string   `help:"Column to sort by: name (default), id, devices, children or parent."`
	Desc     bool     `help:"Sort descending."`
}

func (c *PoliciesListCmd) Run(app *App) error {
	if c.All && c.Parent != "" {
		return errors.New("--all and --parent cannot be combined")
	}
	api, err := app.API()
	if err != nil {
		return err
	}
	pols, err := api.QueryPolicies(app.Ctx, nil)
	if err != nil {
		return err
	}
	idx := newPolicyIndex(pols)

	var (
		scope      []addigy.Policy
		one, many  = "policy", "policies"
		suffix     string
		showParent bool
		rootView   bool
	)
	switch {
	case c.Parent != "":
		parent, err := idx.resolve(c.Parent)
		if err != nil {
			return err
		}
		scope = idx.children[parent.ID]
		one, many = "sub-policy", "sub-policies"
		suffix = fmt.Sprintf(" of %q", parent.Name)
	case c.All || c.Name != "" || len(c.IDs) > 0:
		scope = idx.all
		showParent = true
	default:
		scope = idx.roots
		one, many = "root policy", "root policies"
		rootView = true
	}

	wantID := map[string]bool{}
	for _, id := range c.IDs {
		wantID[strings.ToLower(id)] = true
	}
	needle := strings.ToLower(c.Name)
	shown := make([]addigy.Policy, 0, len(scope))
	for _, p := range scope {
		if len(wantID) > 0 && !wantID[strings.ToLower(p.ID)] {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(p.Name), needle) {
			continue
		}
		shown = append(shown, p)
	}

	var devices map[string]int // devices per policy, including sub-policies
	if !c.NoCounts {
		if devices, err = countDevices(app, api, idx); err != nil {
			return err
		}
	}

	if err := sortPolicyRows(shown, idx, devices, c.Sort, c.Desc); err != nil {
		return err
	}

	if app.json() {
		raws := make([]json.RawMessage, len(shown))
		for i, p := range shown {
			raws[i] = p.Raw
			if devices != nil {
				if raws[i], err = withDeviceCount(p.Raw, devices[p.ID]); err != nil {
					return err
				}
			}
		}
		return output.JSON(app.Out, raws)
	}

	headers := []string{"POLICY ID", "NAME"}
	if devices != nil {
		headers = append(headers, "DEVICES")
	}
	headers = append(headers, "CHILDREN")
	// CSV always carries the PARENT column so its layout doesn't depend on the flags.
	withParent := showParent || app.csv()
	if withParent {
		headers = append(headers, "PARENT")
	}
	rows := make([][]string, 0, len(shown))
	for _, p := range shown {
		row := []string{p.ID, app.cell(p.Name)}
		if devices != nil {
			row = append(row, strconv.Itoa(devices[p.ID]))
		}
		row = append(row, strconv.Itoa(len(idx.children[p.ID])))
		if withParent {
			parent := idx.parentName(p)
			if app.csv() && p.Parent == "" {
				parent = ""
			}
			row = append(row, parent)
		}
		rows = append(rows, row)
	}
	if err := output.Rows(app.Out, app.Format(), headers, rows, app.borders()); err != nil {
		return err
	}
	app.footer("%s%s", count(len(shown), one, many), suffix)
	if rootView && !app.csv() {
		fmt.Fprintln(app.Out, "Use --parent <id|name> for sub-policies, --all for every level, or 'policies tree'.")
	}
	if devices != nil && !app.csv() {
		fmt.Fprintln(app.Out, "DEVICES counts the devices in a policy and all of its sub-policies.")
	}
	return nil
}

// withDeviceCount adds a "deviceCount" field to a raw policy object; every
// other field is passed through untouched.
func withDeviceCount(raw json.RawMessage, n int) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("unexpected policy JSON: %w", err)
	}
	obj["deviceCount"] = json.RawMessage(strconv.Itoa(n))
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// ---- policies tree ---------------------------------------------------------

type PoliciesTreeCmd struct {
	Ref      string `arg:"" optional:"" help:"Start at this policy (ID or name). Default: every root policy."`
	Depth    int    `help:"Levels to show below the starting policies (0 = all)."`
	IDs      bool   `name:"ids" help:"Show policy IDs next to names."`
	NoCounts bool   `name:"no-counts" help:"Skip the device counts (saves fetching every device)."`
}

func (c *PoliciesTreeCmd) Run(app *App) error {
	api, err := app.API()
	if err != nil {
		return err
	}
	pols, err := api.QueryPolicies(app.Ctx, nil)
	if err != nil {
		return err
	}
	idx := newPolicyIndex(pols)

	starts := idx.roots
	if c.Ref != "" {
		p, err := idx.resolve(c.Ref)
		if err != nil {
			return err
		}
		starts = []addigy.Policy{p}
	}
	nodes := idx.buildTree(starts, c.Depth)

	counted := !c.NoCounts
	if counted {
		totals, err := countDevices(app, api, idx)
		if err != nil {
			return err
		}
		setDeviceCounts(nodes, totals)
	}

	switch app.Format() {
	case output.FormatJSON:
		return output.JSON(app.Out, nodes)
	case output.FormatCSV:
		// The hierarchy as flat rows: one per policy, parents before children.
		headers := []string{"POLICY ID", "NAME", "PATH", "DEPTH"}
		if counted {
			headers = append(headers, "DEVICES")
		}
		return output.CSV(app.Out, headers, idx.treeRows(nodes, 0))
	}
	writeTree(app.Out, nodes, c.IDs)
	app.footer("%s", count(countNodes(nodes), "policy", "policies"))
	if counted {
		fmt.Fprintln(app.Out, "[n] = devices in the policy and all of its sub-policies.")
	}
	return nil
}

// ---- policies get ----------------------------------------------------------

type PoliciesGetCmd struct {
	Ref string `arg:"" help:"Policy ID or exact name."`
}

func (c *PoliciesGetCmd) Run(app *App) error {
	if err := app.noCSV("policies get"); err != nil {
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
	p, err := newPolicyIndex(pols).resolve(c.Ref)
	if err != nil {
		return err
	}
	return output.JSON(app.Out, p.Raw)
}

func policyNames(pols []addigy.Policy) map[string]string {
	m := make(map[string]string, len(pols))
	for _, p := range pols {
		m[p.ID] = p.Name
	}
	return m
}

// sortPolicyRows sorts shown by the requested column. The empty column
// defaults to "name". "devices" needs device counts (drop --no-counts).
func sortPolicyRows(shown []addigy.Policy, idx *policyIndex, devices map[string]int, col string, desc bool) error {
	col = strings.ToLower(col)
	if col == "" {
		col = "name"
	}
	switch col {
	case "id", "name", "children", "parent":
	case "devices":
		if devices == nil {
			return errors.New("--sort devices needs device counts; drop --no-counts")
		}
	default:
		return fmt.Errorf("unknown --sort column %q (use one of: id, name, devices, children, parent)", col)
	}
	sort.SliceStable(shown, func(i, j int) bool {
		a, b := shown[i], shown[j]
		var c int
		switch col {
		case "id":
			c = cmpString(a.ID, b.ID)
		case "name":
			c = cmpString(a.Name, b.Name)
		case "devices":
			c = cmpInt(devices[a.ID], devices[b.ID])
		case "children":
			c = cmpInt(len(idx.children[a.ID]), len(idx.children[b.ID]))
		case "parent":
			c = cmpString(idx.parentName(a), idx.parentName(b))
		}
		if c == 0 {
			c = cmpString(a.Name, b.Name)
		}
		if c == 0 {
			c = cmpString(a.ID, b.ID)
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	return nil
}

// count formats "1 policy" / "2 policies".
func count(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
