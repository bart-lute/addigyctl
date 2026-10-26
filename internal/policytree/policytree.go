// Package policytree organizes Addigy's flat policy list into a hierarchy:
// resolving a policy by ID, name or path, walking its sub-policies and
// rendering the tree.
package policytree

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
)

// Index organizes the flat policy list Addigy returns into a hierarchy.
// All lookups are client-side; the API returns every policy in one response.
type Index struct {
	all      []addigy.Policy
	byID     map[string]addigy.Policy
	children map[string][]addigy.Policy // parent ID -> direct children, sorted by name
	roots    []addigy.Policy            // sorted by name
}

// New builds the index. A policy is a root when it has no parent
// (Addigy returns null) or when its parent is not in the result set, so that
// no policy is ever hidden from the default view.
func New(pols []addigy.Policy) *Index {
	x := &Index{
		all:      append([]addigy.Policy(nil), pols...),
		byID:     make(map[string]addigy.Policy, len(pols)),
		children: map[string][]addigy.Policy{},
	}
	for _, p := range pols {
		x.byID[p.ID] = p
	}
	for _, p := range pols {
		_, parentKnown := x.byID[p.Parent]
		if p.Parent != "" && p.Parent != p.ID && parentKnown {
			x.children[p.Parent] = append(x.children[p.Parent], p)
		} else {
			x.roots = append(x.roots, p)
		}
	}
	sortPolicies(x.all)
	sortPolicies(x.roots)
	for id := range x.children {
		sortPolicies(x.children[id])
	}
	return x
}

// Get returns the policy with the given ID.
func (x *Index) Get(id string) (addigy.Policy, bool) {
	p, ok := x.byID[id]
	return p, ok
}

// All returns every policy, sorted by name.
func (x *Index) All() []addigy.Policy { return x.all }

// Roots returns the policies without a (known) parent, sorted by name.
func (x *Index) Roots() []addigy.Policy { return x.roots }

// Children returns the direct sub-policies of a policy, sorted by name.
func (x *Index) Children(id string) []addigy.Policy { return x.children[id] }

func sortPolicies(ps []addigy.Policy) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := strings.ToLower(ps[i].Name), strings.ToLower(ps[j].Name)
		if a != b {
			return a < b
		}
		return ps[i].ID < ps[j].ID
	})
}

// Resolve finds a policy by ID, exact (case-insensitive) name, or full path
// ("Parent / Child"). Names are not guaranteed to be unique, so an ambiguous
// name is an error that lists the candidates with their full path, which can
// then be used as the reference.
func (x *Index) Resolve(ref string) (addigy.Policy, error) {
	if p, ok := x.byID[ref]; ok {
		return p, nil
	}
	var matches []addigy.Policy
	for _, p := range x.all {
		if strings.EqualFold(p.ID, ref) || strings.EqualFold(p.Name, ref) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 && strings.Contains(ref, "/") {
		want := normalizePath(ref)
		for _, p := range x.all {
			if strings.EqualFold(x.Path(p), want) {
				matches = append(matches, p)
			}
		}
	}
	switch len(matches) {
	case 0:
		return addigy.Policy{}, fmt.Errorf("no policy with ID or name %q", ref)
	case 1:
		return matches[0], nil
	}
	lines := make([]string, len(matches))
	for i, p := range matches {
		lines[i] = fmt.Sprintf("  %s  %s", p.ID, x.Path(p))
	}
	return addigy.Policy{}, fmt.Errorf("%q matches %d policies; use the ID instead:\n%s", ref, len(matches), strings.Join(lines, "\n"))
}

// normalizePath turns "a/b" or "a /b" into "a / b".
func normalizePath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, s := range parts {
		parts[i] = strings.TrimSpace(s)
	}
	return strings.Join(parts, " / ")
}

// Subtree returns the policy with the given ID followed by all of its
// descendants (depth-first, siblings sorted by name).
func (x *Index) Subtree(id string) []addigy.Policy {
	root, ok := x.byID[id]
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []addigy.Policy
	var walk func(p addigy.Policy)
	walk = func(p addigy.Policy) {
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		out = append(out, p)
		for _, k := range x.children[p.ID] {
			walk(k)
		}
	}
	walk(root)
	return out
}

// Members returns the IDs of the policies a device can be located in to
// count as "in" the policy with the given ID: that policy and, unless direct
// is set, all of its sub-policies.
func (x *Index) Members(id string, direct bool) map[string]bool {
	ps := x.Subtree(id)
	if direct && len(ps) > 0 {
		ps = ps[:1]
	}
	m := make(map[string]bool, len(ps))
	for _, p := range ps {
		m[p.ID] = true
	}
	return m
}

// Path returns "Root / Parent / Policy".
func (x *Index) Path(p addigy.Policy) string {
	parts := []string{p.Name}
	seen := map[string]bool{p.ID: true}
	for cur := p; cur.Parent != ""; {
		parent, ok := x.byID[cur.Parent]
		if !ok || seen[parent.ID] {
			break
		}
		seen[parent.ID] = true
		parts = append([]string{parent.Name}, parts...)
		cur = parent
	}
	return strings.Join(parts, " / ")
}

// ParentName returns the display name of a policy's parent, or "-" for roots.
func (x *Index) ParentName(p addigy.Policy) string {
	if p.Parent == "" {
		return "-"
	}
	if parent, ok := x.byID[p.Parent]; ok && parent.Name != "" {
		return parent.Name
	}
	return p.Parent
}

// Node is one policy in a rendered hierarchy (also the --json shape).
type Node struct {
	ID   string `json:"policyId"`
	Name string `json:"name"`
	// DeviceCount is the number of devices located in the policy or any of its
	// sub-policies; nil when counts were not requested.
	DeviceCount *int   `json:"deviceCount,omitempty"`
	Children    []Node `json:"children,omitempty"`
}

// BuildTree returns the hierarchy below each of the given policies.
// maxDepth is the number of levels shown below them; 0 means unlimited.
func (x *Index) BuildTree(starts []addigy.Policy, maxDepth int) []Node {
	seen := map[string]bool{}
	nodes := make([]Node, 0, len(starts))
	for _, p := range starts {
		if seen[p.ID] {
			continue
		}
		nodes = append(nodes, x.node(p, 0, maxDepth, seen))
	}
	return nodes
}

func (x *Index) node(p addigy.Policy, level, maxDepth int, seen map[string]bool) Node {
	n := Node{ID: p.ID, Name: p.Name}
	seen[p.ID] = true
	if maxDepth == 0 || level < maxDepth {
		for _, k := range x.children[p.ID] {
			if !seen[k.ID] { // guards against cycles in bad data
				n.Children = append(n.Children, x.node(k, level+1, maxDepth, seen))
			}
		}
	}
	return n
}

// SetDeviceCounts fills in DeviceCount on every node.
func SetDeviceCounts(nodes []Node, totals map[string]int) {
	for i := range nodes {
		n := totals[nodes[i].ID]
		nodes[i].DeviceCount = &n
		SetDeviceCounts(nodes[i].Children, totals)
	}
}

// Rows flattens nodes (depth first) into CSV rows: ID, name, full path,
// depth below the starting policy (0 = a starting policy) and, when counted,
// devices.
func (x *Index) Rows(nodes []Node, depth int) [][]string {
	var rows [][]string
	for _, n := range nodes {
		path := n.Name
		if p, ok := x.byID[n.ID]; ok {
			path = x.Path(p)
		}
		row := []string{n.ID, n.Name, path, strconv.Itoa(depth)}
		if n.DeviceCount != nil {
			row = append(row, strconv.Itoa(*n.DeviceCount))
		}
		rows = append(rows, row)
		rows = append(rows, x.Rows(n.Children, depth+1)...)
	}
	return rows
}

func CountNodes(nodes []Node) int {
	n := len(nodes)
	for _, c := range nodes {
		n += CountNodes(c.Children)
	}
	return n
}

// Write renders nodes as an indented tree with box-drawing connectors.
func Write(w io.Writer, nodes []Node, showIDs bool) {
	for _, n := range nodes {
		fmt.Fprintln(w, treeLabel(n, showIDs))
		writeChildren(w, n.Children, "", showIDs)
	}
}

func writeChildren(w io.Writer, kids []Node, prefix string, showIDs bool) {
	for i, k := range kids {
		connector, next := "├── ", "│   "
		if i == len(kids)-1 {
			connector, next = "└── ", "    "
		}
		fmt.Fprintln(w, prefix+connector+treeLabel(k, showIDs))
		writeChildren(w, k.Children, prefix+next, showIDs)
	}
}

func treeLabel(n Node, showIDs bool) string {
	name := n.Name
	if name == "" {
		name = "(unnamed)"
	}
	if showIDs {
		name = fmt.Sprintf("%s (%s)", name, n.ID)
	}
	if n.DeviceCount != nil {
		name += fmt.Sprintf(" [%d]", *n.DeviceCount)
	}
	return name
}
