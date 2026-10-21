package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
	"github.com/ginkio/addigyctl/internal/policytree"
)

// fetchPageSize is the page size used when fetching all devices. It is a
// variable so tests can force multiple pages.
var fetchPageSize = 50

// resolvePolicy fetches the policy list and resolves ref (ID, name or
// "Parent / Child" path) to a policy.
func resolvePolicy(app *App, api *addigy.API, ref string) (addigy.Policy, *policytree.Index, error) {
	pols, err := api.QueryPolicies(app.Ctx, nil)
	if err != nil {
		return addigy.Policy{}, nil, err
	}
	idx := policytree.New(pols)
	p, err := idx.Resolve(ref)
	return p, idx, err
}

// countDevices returns, for every policy in idx, the number of devices located
// in it or in any of its sub-policies (the same set `devices list --policy`
// shows). It needs one bulk fetch of every device, asking only for the
// location fact.
func countDevices(app *App, api *addigy.API, idx *policytree.Index) (map[string]int, error) {
	all, reported, err := api.AllDevices(app.Ctx, addigy.DeviceQuery{
		Facts:     []string{addigy.LocationFact},
		SortField: addigy.StableSortField,
		PerPage:   fetchPageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("counting devices: %w", err)
	}
	if reported > 0 && len(all) != reported {
		fmt.Fprintf(app.Err, "warning: Addigy reported %d devices but %d were received; the device counts may be incomplete\n", reported, len(all))
	}
	direct := map[string]int{}
	for _, d := range all {
		if v, ok := d.Value(addigy.LocationFact); ok {
			if id, ok := v.(string); ok {
				direct[id]++
			}
		}
	}
	return idx.RollUp(direct), nil
}

// withFact returns facts plus id (if not already present), without modifying
// the input slice.
func withFact(facts []string, id string) []string {
	for _, f := range facts {
		if f == id {
			return facts
		}
	}
	return append(append([]string(nil), facts...), id)
}

// sortDevices orders devices by a fact value. Numbers compare numerically,
// everything else case-insensitively as text. Devices without the fact go last
// in either direction; ties are broken by agent ID.
func sortDevices(ds []addigy.Device, field string, desc bool) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		av, aok := a.Value(field)
		bv, bok := b.Value(field)
		if aok != bok {
			return aok
		}
		if !aok {
			return a.AgentID < b.AgentID
		}
		c := compareValues(av, bv)
		if c == 0 {
			return a.AgentID < b.AgentID
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
}

func compareValues(a, b any) int {
	if af, ok := a.(float64); ok {
		if bf, ok := b.(float64); ok {
			switch {
			case af < bf:
				return -1
			case af > bf:
				return 1
			}
			return 0
		}
	}
	return strings.Compare(strings.ToLower(output.Value(a)), strings.ToLower(output.Value(b)))
}

// paginate slices an already complete result set. With all set it returns
// everything as one page.
func paginate(ds []addigy.Device, page, perPage int, all bool) ([]addigy.Device, addigy.PageMetadata) {
	total := len(ds)
	if all || perPage <= 0 {
		return ds, addigy.PageMetadata{Page: 1, PageCount: 1, PerPage: total, ResultCount: total, Total: total}
	}
	if page < 1 {
		page = 1
	}
	pageCount := (total + perPage - 1) / perPage
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	shown := ds[start:end]
	return shown, addigy.PageMetadata{
		Page: page, PageCount: pageCount, PerPage: perPage, ResultCount: len(shown), Total: total,
	}
}
