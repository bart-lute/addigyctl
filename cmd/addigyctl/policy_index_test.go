package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ginkio/addigyctl/internal/addigy"
)

// testPolicies builds:
//
//	Acme
//	├── Finance
//	│   └── Laptops
//	└── Sales
//	    └── Laptops      (same name, different parent)
//	Orphan               (parent not in the result set)
func testPolicies() []addigy.Policy {
	return []addigy.Policy{
		{ID: "sales-laptops", Name: "Laptops", Parent: "sales"},
		{ID: "acme", Name: "Acme"},
		{ID: "finance", Name: "Finance", Parent: "acme"},
		{ID: "fin-laptops", Name: "Laptops", Parent: "finance"},
		{ID: "sales", Name: "Sales", Parent: "acme"},
		{ID: "orphan", Name: "Orphan", Parent: "gone"},
	}
}

func names(ps []addigy.Policy) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return strings.Join(out, ",")
}

func TestIndexRootsAndChildren(t *testing.T) {
	x := newPolicyIndex(testPolicies())
	if got := names(x.roots); got != "Acme,Orphan" {
		t.Errorf("roots = %s, want Acme,Orphan (orphans count as roots)", got)
	}
	if got := names(x.children["acme"]); got != "Finance,Sales" {
		t.Errorf("children of acme = %s", got)
	}
	if got := len(x.children["finance"]); got != 1 {
		t.Errorf("finance children = %d", got)
	}
}

func TestSelfParentIsRoot(t *testing.T) {
	x := newPolicyIndex([]addigy.Policy{{ID: "a", Name: "A", Parent: "a"}})
	if len(x.roots) != 1 {
		t.Errorf("a self-parented policy should be a root, roots = %v", names(x.roots))
	}
}

func TestResolve(t *testing.T) {
	x := newPolicyIndex(testPolicies())

	if p, err := x.resolve("fin-laptops"); err != nil || p.Name != "Laptops" {
		t.Errorf("by id: %v %v", p.Name, err)
	}
	if p, err := x.resolve("SALES"); err != nil || p.ID != "sales" {
		t.Errorf("by name (case-insensitive): %v %v", p.ID, err)
	}
	if p, err := x.resolve("ACME"); err != nil || p.ID != "acme" {
		t.Errorf("by name: %v %v", p.ID, err)
	}
	if _, err := x.resolve("nope"); err == nil {
		t.Error("expected error for unknown policy")
	}

	_, err := x.resolve("laptops")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	for _, want := range []string{"fin-laptops", "sales-laptops", "Acme / Finance / Laptops", "Acme / Sales / Laptops"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguity error missing %q:\n%v", want, err)
		}
	}
}

func TestResolveByPath(t *testing.T) {
	x := newPolicyIndex(testPolicies())
	for ref, want := range map[string]string{
		"Acme / Sales / Laptops": "sales-laptops",
		"acme/finance/laptops":   "fin-laptops",
		"Acme / Finance":         "finance",
	} {
		if p, err := x.resolve(ref); err != nil || p.ID != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", ref, p.ID, err, want)
		}
	}
	if _, err := x.resolve("Acme / Nope / Laptops"); err == nil {
		t.Error("expected error for unknown path")
	}
}

func TestSubtree(t *testing.T) {
	x := newPolicyIndex(testPolicies())
	if got := names(x.subtree("acme")); got != "Acme,Finance,Laptops,Sales,Laptops" {
		t.Errorf("subtree(acme) = %s", got)
	}
	if got := names(x.subtree("sales-laptops")); got != "Laptops" {
		t.Errorf("a leaf's subtree is itself, got %s", got)
	}
	if got := x.subtree("missing"); got != nil {
		t.Errorf("unknown id should give nil, got %v", got)
	}

	cyclic := newPolicyIndex([]addigy.Policy{
		{ID: "a", Name: "A", Parent: "b"},
		{ID: "b", Name: "B", Parent: "a"},
	})
	if got := names(cyclic.subtree("a")); got != "A,B" {
		t.Errorf("cyclic subtree = %s", got)
	}
}

func TestPathCycleGuard(t *testing.T) {
	x := newPolicyIndex([]addigy.Policy{
		{ID: "a", Name: "A", Parent: "b"},
		{ID: "b", Name: "B", Parent: "a"},
	})
	if got := x.path(x.byID["a"]); got != "B / A" {
		t.Errorf("path = %q", got)
	}
}

func TestParentName(t *testing.T) {
	x := newPolicyIndex(testPolicies())
	if got := x.parentName(x.byID["finance"]); got != "Acme" {
		t.Errorf("got %q", got)
	}
	if got := x.parentName(x.byID["acme"]); got != "-" {
		t.Errorf("got %q", got)
	}
	if got := x.parentName(x.byID["orphan"]); got != "gone" {
		t.Errorf("unknown parent should fall back to its ID, got %q", got)
	}
}

func TestWriteTree(t *testing.T) {
	x := newPolicyIndex(testPolicies())
	var buf bytes.Buffer
	nodes := x.buildTree(x.roots, 0)
	writeTree(&buf, nodes, false)
	want := "Acme\n" +
		"├── Finance\n" +
		"│   └── Laptops\n" +
		"└── Sales\n" +
		"    └── Laptops\n" +
		"Orphan\n"
	if buf.String() != want {
		t.Errorf("tree:\n%s\nwant:\n%s", buf.String(), want)
	}
	if got := countNodes(nodes); got != 6 {
		t.Errorf("countNodes = %d, want 6", got)
	}
}

func TestTreeDepthAndIDs(t *testing.T) {
	x := newPolicyIndex(testPolicies())
	var buf bytes.Buffer
	writeTree(&buf, x.buildTree([]addigy.Policy{x.byID["acme"]}, 1), true)
	want := "Acme (acme)\n├── Finance (finance)\n└── Sales (sales)\n"
	if buf.String() != want {
		t.Errorf("depth-limited tree:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestTreeCycleDoesNotLoop(t *testing.T) {
	x := newPolicyIndex([]addigy.Policy{
		{ID: "a", Name: "A", Parent: "b"},
		{ID: "b", Name: "B", Parent: "a"},
	})
	// Neither is a root; starting from one must still terminate.
	nodes := x.buildTree([]addigy.Policy{x.byID["a"]}, 0)
	if got := countNodes(nodes); got != 2 {
		t.Errorf("countNodes = %d, want 2", got)
	}
}

func TestTreeJSONShape(t *testing.T) {
	x := newPolicyIndex(testPolicies())
	b, err := json.Marshal(x.buildTree([]addigy.Policy{x.byID["finance"]}, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"policyId":"finance","name":"Finance","children":[{"policyId":"fin-laptops","name":"Laptops"}]}]`
	if string(b) != want {
		t.Errorf("got %s\nwant %s", b, want)
	}
}

func TestRollUpIncludesSubPolicies(t *testing.T) {
	x := newPolicyIndex(testPolicies())
	got := x.rollUp(map[string]int{"acme": 1, "finance": 2, "fin-laptops": 4, "sales-laptops": 8, "unknown": 100})
	want := map[string]int{"acme": 15, "finance": 6, "fin-laptops": 4, "sales": 8, "sales-laptops": 8, "orphan": 0}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("total[%s] = %d, want %d", id, got[id], n)
		}
	}
	if len(got) != len(x.all) {
		t.Errorf("every policy needs a total, got %v", got)
	}
}

func TestTreeLabelWithCounts(t *testing.T) {
	three := 3
	zero := 0
	if got := treeLabel(treeNode{ID: "a", Name: "A", DeviceCount: &three}, false); got != "A [3]" {
		t.Errorf("got %q", got)
	}
	if got := treeLabel(treeNode{ID: "a", Name: "A", DeviceCount: &zero}, true); got != "A (a) [0]" {
		t.Errorf("got %q", got)
	}
	if got := treeLabel(treeNode{ID: "a"}, false); got != "(unnamed)" {
		t.Errorf("got %q", got)
	}
}
