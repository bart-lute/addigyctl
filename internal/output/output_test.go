package output

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "-"},
		{"", "-"},
		{"macOS 15.6", "macOS 15.6"},
		{true, "true"},
		{float64(42), "42"},
		{float64(1.5), "1.5"},
		{[]any{"a", "b"}, `["a","b"]`},
		{map[string]any{"k": 1.0}, `{"k":1}`},
	}
	for _, c := range cases {
		if got := Value(c.in); got != c.want {
			t.Errorf("Value(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("abcdef", 4); got != "abc…" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("abc", 4); got != "abc" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("héllo wörld", 6); got != "héllo…" {
		t.Errorf("got %q", got)
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	err := Table(&buf, []string{"ID", "NAME"}, [][]string{{"1", "alpha\nbeta"}, {"22", "x"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "ID  NAME\n1   alpha beta\n22  x\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestJSONPassesRawMessageThrough(t *testing.T) {
	var buf bytes.Buffer
	raw := json.RawMessage(`{"a":1,"b":"<x>"}`)
	if err := JSON(&buf, raw); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": 1,\n  \"b\": \"<x>\"\n}\n"
	if buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}

func TestField(t *testing.T) {
	for in, want := range map[any]string{nil: "", "": "", "x": "x", true: "true", 4.0: "4"} {
		if got := Field(in); got != want {
			t.Errorf("Field(%#v) = %q, want %q", in, got, want)
		}
	}
	if got := Field([]any{"a"}); got != `["a"]` {
		t.Errorf("got %q", got)
	}
}

func TestCSVQuotesWhenNeeded(t *testing.T) {
	var buf bytes.Buffer
	err := CSV(&buf, []string{"ID", "NAME"}, [][]string{
		{"1", "plain"},
		{"2", "a,b"},
		{"3", `say "hi"`},
		{"4", "two\nlines"}, // newlines are kept in CSV (unlike tables)
		{"5", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "ID,NAME\n1,plain\n2,\"a,b\"\n3,\"say \"\"hi\"\"\"\n4,\"two\nlines\"\n5,\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRowsPicksTheFormat(t *testing.T) {
	headers, rows := []string{"A", "B"}, [][]string{{"1", "x y"}}

	var tbl, csvOut bytes.Buffer
	if err := Rows(&tbl, FormatTable, headers, rows); err != nil {
		t.Fatal(err)
	}
	if err := Rows(&csvOut, FormatCSV, headers, rows); err != nil {
		t.Fatal(err)
	}
	if tbl.String() != "A  B\n1  x y\n" {
		t.Errorf("table = %q", tbl.String())
	}
	if csvOut.String() != "A,B\n1,x y\n" {
		t.Errorf("csv = %q", csvOut.String())
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"": FormatTable, "table": FormatTable, " CSV ": FormatCSV, "Json": FormatJSON} {
		if got, err := ParseFormat(in); err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Error("expected an error for an unknown format")
	}
}
