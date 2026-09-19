// Package output renders command results as tables, CSV or JSON.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Format is an output format.
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatCSV   Format = "csv"
)

// ParseFormat parses a --output value. The empty string means the default
// (table).
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimSpace(s))); f {
	case "":
		return FormatTable, nil
	case FormatTable, FormatJSON, FormatCSV:
		return f, nil
	default:
		return "", fmt.Errorf("unknown output format %q (use table, json or csv)", s)
	}
}

// Rows writes tabular data as a table or as CSV. (JSON is not tabular; callers
// handle it themselves.)
func Rows(w io.Writer, f Format, headers []string, rows [][]string) error {
	if f == FormatCSV {
		return CSV(w, headers, rows)
	}
	return Table(w, headers, rows)
}

// CSV writes a header row followed by the rows as RFC 4180 CSV. Cells are
// written as they are: quoted when they contain commas, quotes or newlines.
func CSV(w io.Writer, headers []string, rows [][]string) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(headers); err != nil {
		return err
	}
	if err := cw.WriteAll(rows); err != nil { // WriteAll flushes
		return err
	}
	return cw.Error()
}

// JSON writes v as indented JSON. json.RawMessage values are passed through
// unchanged, so API responses are shown exactly as received.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Table writes an aligned, whitespace-separated table.
func Table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, joinCells(headers))
	for _, r := range rows {
		fmt.Fprintln(tw, joinCells(r))
	}
	return tw.Flush()
}

func joinCells(cells []string) string {
	clean := make([]string, len(cells))
	for i, c := range cells {
		c = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(c)
		clean[i] = c
	}
	return strings.Join(clean, "\t")
}

// Value renders a loosely typed JSON value (such as a device fact value) as
// a single-line string for a table cell. Empty values render as "-".
func Value(v any) string {
	if s := Field(v); s != "" {
		return s
	}
	return "-"
}

// Field renders a loosely typed JSON value as a CSV field: like Value, but
// empty values stay empty instead of becoming "-".
func Field(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

// Truncate shortens s to at most max runes, ending with an ellipsis.
func Truncate(s string, max int) string {
	if max <= 1 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
