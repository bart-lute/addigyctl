package main

import "testing"

func TestLineDiff(t *testing.T) {
	if d := lineDiff("a\nb\n", "a\nb\n", 1); d != "" {
		t.Errorf("equal texts: %q", d)
	}
	a := "VERSION=4.15.0\n1\n2\n3\n4\n5\nend\n"
	b := "VERSION=4.16.0\n1\n2\n3\n4\n5\nend\nextra\n"
	want := "-VERSION=4.15.0\n+VERSION=4.16.0\n 1\n…\n end\n+extra\n"
	if d := lineDiff(a, b, 1); d != want {
		t.Errorf("diff =\n%s\nwant\n%s", d, want)
	}
	if d := lineDiff("", "new\n", 2); d != "+new\n" {
		t.Errorf("from empty: %q", d)
	}
}
