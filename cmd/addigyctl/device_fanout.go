package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

const (
	// fanOutWorkers bounds how many page requests run at once.
	fanOutWorkers = 4

	// locationFact is the device fact holding the policy the device is located
	// in. (policy_ids, in contrast, lists every policy that applies to it.)
	locationFact = "policy_id"

	// stableSortField is the server-side order used while paging through all
	// devices, so parallel page requests neither overlap nor skip devices.
	stableSortField = "serial_number"
)

// fetchPageSize is the page size used when fetching all devices. It is a
// variable so tests can force multiple pages.
var fetchPageSize = 50

// resolvePolicy fetches the policy list and resolves ref (ID, name or
// "Parent / Child" path) to a policy.
func resolvePolicy(app *App, api *addigy.API, ref string) (addigy.Policy, *policyIndex, error) {
	pols, err := api.QueryPolicies(app.Ctx, nil)
	if err != nil {
		return addigy.Policy{}, nil, err
	}
	idx := newPolicyIndex(pols)
	p, err := idx.resolve(ref)
	return p, idx, err
}

// fetchAllDevices returns every device matching q, fetching the pages after
// the first in parallel. It also returns the total Addigy reported, so callers
// can detect an incomplete result.
func fetchAllDevices(ctx context.Context, api *addigy.API, q addigy.DeviceQuery) ([]addigy.Device, int, error) {
	q.Page = 1
	first, err := api.SearchDevices(ctx, q)
	if err != nil {
		return nil, 0, err
	}

	pageCount := first.Metadata.PageCount
	pages := make([][]addigy.Device, max(pageCount, 1))
	errs := make([]error, len(pages))
	pages[0] = first.Items

	sem := make(chan struct{}, fanOutWorkers)
	var wg sync.WaitGroup
	for p := 2; p <= pageCount; p++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(p int) {
			defer wg.Done()
			defer func() { <-sem }()
			pq := q
			pq.Page = p
			pg, err := api.SearchDevices(ctx, pq)
			if err != nil {
				errs[p-1] = err
				return
			}
			pages[p-1] = pg.Items
		}(p)
	}
	wg.Wait()

	seen := map[string]bool{}
	var all []addigy.Device
	for i, items := range pages {
		if errs[i] != nil {
			return nil, 0, errs[i]
		}
		for _, d := range items {
			if seen[d.AgentID] {
				continue
			}
			seen[d.AgentID] = true
			all = append(all, d)
		}
	}
	return all, first.Metadata.Total, nil
}

// countDevices returns, for every policy in idx, the number of devices located
// in it or in any of its sub-policies (the same set `devices list --policy`
// shows). It needs one bulk fetch of every device, asking only for the
// location fact.
func countDevices(app *App, api *addigy.API, idx *policyIndex) (map[string]int, error) {
	all, reported, err := fetchAllDevices(app.Ctx, api, addigy.DeviceQuery{
		Facts:     []string{locationFact},
		SortField: stableSortField,
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
		if v, ok := factValue(d, locationFact); ok {
			if id, ok := v.(string); ok {
				direct[id]++
			}
		}
	}
	return idx.rollUp(direct), nil
}

// locatedIn keeps the devices whose location is one of policyIDs.
func locatedIn(ds []addigy.Device, policyIDs map[string]bool) []addigy.Device {
	var out []addigy.Device
	for _, d := range ds {
		if v, ok := factValue(d, locationFact); ok {
			if id, ok := v.(string); ok && policyIDs[id] {
				out = append(out, d)
			}
		}
	}
	return out
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
		av, aok := factValue(a, field)
		bv, bok := factValue(b, field)
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

func factValue(d addigy.Device, id string) (any, bool) {
	f, ok := d.Facts[id]
	if !ok || f.Value == nil {
		return nil, false
	}
	return f.Value, true
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
