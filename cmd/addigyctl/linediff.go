package main

import (
	"fmt"
	"strings"
)

// lineDiff renders a unified-style diff of two texts: changed lines with
// context lines around them, "-" for removed and "+" for added, and "…"
// between separate hunks. It is "" when the texts are equal. Meant for
// scripts, which are small: it is a plain O(n·m) LCS.
func lineDiff(a, b string, context int) string {
	if a == b {
		return ""
	}
	x, y := splitLines(a), splitLines(b)
	// lcs[i][j] is the LCS length of x[i:] and y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	type op struct {
		kind byte // ' ', '-' or '+'
		line string
	}
	var ops []op
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			ops = append(ops, op{' ', x[i]})
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', x[i]}) // removals before additions
			i++
		default:
			ops = append(ops, op{'+', y[j]})
			j++
		}
	}

	// Keep changes plus up to context unchanged lines around each.
	keep := make([]bool, len(ops))
	for k, o := range ops {
		if o.kind != ' ' {
			for c := max(0, k-context); c <= min(len(ops)-1, k+context); c++ {
				keep[c] = true
			}
		}
	}
	var sb strings.Builder
	gap := false
	for k, o := range ops {
		if !keep[k] {
			gap = true
			continue
		}
		if gap && sb.Len() > 0 {
			sb.WriteString("…\n")
		}
		gap = false
		fmt.Fprintf(&sb, "%c%s\n", o.kind, o.line)
	}
	return sb.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
