package policytree

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
	x := New(testPolicies())
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
	x := New([]addigy.Policy{{ID: "a", Name: "A", Parent: "a"}})
	if len(x.roots) != 1 {
		t.Errorf("a self-parented policy should be a root, roots = %v", names(x.roots))
	}
}

func TestResolve(t *testing.T) {
	x := New(testPolicies())

	if p, err := x.Resolve("fin-laptops"); err != nil || p.Name != "Laptops" {
		t.Errorf("by id: %v %v", p.Name, err)
	}
	if p, err := x.Resolve("SALES"); err != nil || p.ID != "sales" {
		t.Errorf("by name (case-insensitive): %v %v", p.ID, err)
	}
	if p, err := x.Resolve("ACME"); err != nil || p.ID != "acme" {
		t.Errorf("by name: %v %v", p.ID, err)
	}
	if _, err := x.Resolve("nope"); err == nil {
		t.Error("expected error for unknown policy")
	}

	_, err := x.Resolve("laptops")
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
	x := New(testPolicies())
	for ref, want := range map[string]string{
		"Acme / Sales / Laptops": "sales-laptops",
		"acme/finance/laptops":   "fin-laptops",
		"Acme / Finance":         "finance",
	} {
		if p, err := x.Resolve(ref); err != nil || p.ID != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", ref, p.ID, err, want)
		}
	}
	if _, err := x.Resolve("Acme / Nope / Laptops"); err == nil {
		t.Error("expected error for unknown path")
	}
}

func TestSubtree(t *testing.T) {
	x := New(testPolicies())
	if got := names(x.Subtree("acme")); got != "Acme,Finance,Laptops,Sales,Laptops" {
		t.Errorf("subtree(acme) = %s", got)
	}
	if got := names(x.Subtree("sales-laptops")); got != "Laptops" {
		t.Errorf("a leaf's subtree is itself, got %s", got)
	}
	if got := x.Subtree("missing"); got != nil {
		t.Errorf("unknown id should give nil, got %v", got)
	}

	cyclic := New([]addigy.Policy{
		{ID: "a", Name: "A", Parent: "b"},
		{ID: "b", Name: "B", Parent: "a"},
	})
	if got := names(cyclic.Subtree("a")); got != "A,B" {
		t.Errorf("cyclic subtree = %s", got)
	}
}

func TestPathCycleGuard(t *testing.T) {
	x := New([]addigy.Policy{
		{ID: "a", Name: "A", Parent: "b"},
		{ID: "b", Name: "B", Parent: "a"},
	})
	if got := x.Path(x.byID["a"]); got != "B / A" {
		t.Errorf("path = %q", got)
	}
}

func TestParentName(t *testing.T) {
	x := New(testPolicies())
	if got := x.ParentName(x.byID["finance"]); got != "Acme" {
		t.Errorf("got %q", got)
	}
	if got := x.ParentName(x.byID["acme"]); got != "-" {
		t.Errorf("got %q", got)
	}
	if got := x.ParentName(x.byID["orphan"]); got != "gone" {
		t.Errorf("unknown parent should fall back to its ID, got %q", got)
	}
}

func TestWriteTree(t *testing.T) {
	x := New(testPolicies())
	var buf bytes.Buffer
	nodes := x.BuildTree(x.roots, 0)
	Write(&buf, nodes, false)
	want := "Acme\n" +
		"├── Finance\n" +
		"│   └── Laptops\n" +
		"└── Sales\n" +
		"    └── Laptops\n" +
		"Orphan\n"
	if buf.String() != want {
		t.Errorf("tree:\n%s\nwant:\n%s", buf.String(), want)
	}
	if got := CountNodes(nodes); got != 6 {
		t.Errorf("CountNodes = %d, want 6", got)
	}
}

func TestTreeDepthAndIDs(t *testing.T) {
	x := New(testPolicies())
	var buf bytes.Buffer
	Write(&buf, x.BuildTree([]addigy.Policy{x.byID["acme"]}, 1), true)
	want := "Acme (acme)\n├── Finance (finance)\n└── Sales (sales)\n"
	if buf.String() != want {
		t.Errorf("depth-limited tree:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestTreeCycleDoesNotLoop(t *testing.T) {
	x := New([]addigy.Policy{
		{ID: "a", Name: "A", Parent: "b"},
		{ID: "b", Name: "B", Parent: "a"},
	})
	// Neither is a root; starting from one must still terminate.
	nodes := x.BuildTree([]addigy.Policy{x.byID["a"]}, 0)
	if got := CountNodes(nodes); got != 2 {
		t.Errorf("CountNodes = %d, want 2", got)
	}
}

func TestTreeJSONShape(t *testing.T) {
	x := New(testPolicies())
	b, err := json.Marshal(x.BuildTree([]addigy.Policy{x.byID["finance"]}, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"policyId":"finance","name":"Finance","children":[{"policyId":"fin-laptops","name":"Laptops"}]}]`
	if string(b) != want {
		t.Errorf("got %s\nwant %s", b, want)
	}
}

func TestTreeLabelWithCounts(t *testing.T) {
	three := 3
	zero := 0
	if got := treeLabel(Node{ID: "a", Name: "A", DeviceCount: &three}, false); got != "A [3]" {
		t.Errorf("got %q", got)
	}
	if got := treeLabel(Node{ID: "a", Name: "A", DeviceCount: &zero}, true); got != "A (a) [0]" {
		t.Errorf("got %q", got)
	}
	if got := treeLabel(Node{ID: "a"}, false); got != "(unnamed)" {
		t.Errorf("got %q", got)
	}
}

func TestMembers(t *testing.T) {
	x := New(testPolicies())
	if got := x.Members("finance", false); len(got) != 2 || !got["finance"] || !got["fin-laptops"] {
		t.Errorf("Members(finance) = %v", got)
	}
	if got := x.Members("finance", true); len(got) != 1 || !got["finance"] {
		t.Errorf("Members(finance, direct) = %v", got)
	}
	if got := x.Members("missing", false); len(got) != 0 {
		t.Errorf("Members(missing) = %v", got)
	}
}
