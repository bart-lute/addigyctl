package main

import (
	"strings"
	"time"
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
