package main

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// cmpString compares strings case-insensitively: -1, 0 or 1.
func cmpString(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// cmpInt compares ints: -1, 0 or 1.
func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// cmpBool orders false before true.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	default:
		return 1
	}
}

// cmpTime compares times: -1, 0 or 1. Zero times (unset or unparsed) sort first.
func cmpTime(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	default:
		return 0
	}
}

// cmpVersion compares version strings naturally: runs of digits compare as
// numbers, so "9.2" < "10.0" and "4.9" < "4.10". Everything else compares
// case-insensitively, and a version that is a prefix of another sorts first
// ("1.0" < "1.0.1").
func cmpVersion(a, b string) int {
	as, bs := versionParts(a), versionParts(b)
	for i := range min(len(as), len(bs)) {
		x, y := as[i], bs[i]
		xn, xerr := strconv.ParseUint(x, 10, 64)
		yn, yerr := strconv.ParseUint(y, 10, 64)
		var n int
		if xerr == nil && yerr == nil {
			n = cmpInt(int(min(xn, 1<<62)), int(min(yn, 1<<62)))
		} else {
			n = cmpString(x, y)
		}
		if n != 0 {
			return n
		}
	}
	return cmpInt(len(as), len(bs))
}

// versionParts splits a version into alternating runs of digits and
// non-digits: "4.48.0b5" -> ["4" "." "48" "." "0" "b" "5"].
func versionParts(v string) []string {
	var parts []string
	start := 0
	for i, r := range v {
		if i > start && unicode.IsDigit(r) != unicode.IsDigit(rune(v[start])) {
			parts = append(parts, v[start:i])
			start = i
		}
	}
	if start < len(v) {
		parts = append(parts, v[start:])
	}
	return parts
}
