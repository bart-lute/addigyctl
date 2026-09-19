package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/ginkio/addigyctl/internal/addigy"
)

func TestGlobalsFormat(t *testing.T) {
	cases := []struct {
		g       Globals
		want    string
		wantErr string
	}{
		{Globals{}, "table", ""},
		{Globals{Output: "table"}, "table", ""},
		{Globals{Output: "CSV"}, "csv", ""},
		{Globals{Output: "json"}, "json", ""},
		{Globals{JSON: true}, "json", ""},
		{Globals{JSON: true, Output: "json"}, "json", ""},
		{Globals{JSON: true, Output: "csv"}, "", "--json cannot be combined with --output csv"},
		{Globals{Output: "xml"}, "", `unknown output format "xml"`},
	}
	for _, c := range cases {
		got, err := c.g.Format()
		switch {
		case c.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%+v: err = %v, want %q", c.g, err, c.wantErr)
			}
		case err != nil || string(got) != c.want:
			t.Errorf("%+v: got %q, %v; want %q", c.g, got, err, c.want)
		}
	}
}

func TestNewAppRejectsBadOutputFormat(t *testing.T) {
	_, err := newApp(context.Background(), &Globals{Output: "yaml", ConfigFile: t.TempDir() + "/none.json"})
	if err == nil || !strings.Contains(err.Error(), "unknown output format") {
		t.Errorf("got %v", err)
	}
}

func parseCSV(t *testing.T, s string) [][]string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(s)).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v\n%s", err, s)
	}
	return recs
}

func TestDevicesListCSV(t *testing.T) {
	ds := newDeviceServer(t)
	var out, errOut bytes.Buffer
	app := &App{
		Ctx: context.Background(),
		G:   &Globals{APIKey: "k", BaseURL: ds.URL + "/api/v2", Output: "csv"},
		Out: &out, Err: &errOut,
	}
	if err := (&DevicesListCmd{Facts: []string{"serial_number", "device_name"}, Sort: "serial_number"}).Run(app); err != nil {
		t.Fatal(err)
	}
	recs := parseCSV(t, out.String())
	if strings.Join(recs[0], "|") != "AGENT ID|SERIAL NUMBER|DEVICE NAME" {
		t.Errorf("header = %v", recs[0])
	}
	if len(recs) != 1+len(fixtureDevices) {
		t.Errorf("want %d rows, got %d:\n%s", len(fixtureDevices), len(recs)-1, out.String())
	}
	// The fake devices have no device_name: an empty field in CSV, not "-".
	if got := strings.Join(recs[1], "|"); got != "d2|A-1|" {
		t.Errorf("first row = %q", got)
	}
	if strings.Contains(out.String(), "devices") || strings.Contains(out.String(), "-\n") {
		t.Errorf("CSV must not contain a footer or placeholders:\n%s", out.String())
	}
}

func TestDevicesListCSVWithPolicyHasLocationAndNoFooter(t *testing.T) {
	ds := newDeviceServer(t)
	var out bytes.Buffer
	app := &App{
		Ctx: context.Background(),
		G:   &Globals{APIKey: "k", BaseURL: ds.URL + "/api/v2", Output: "csv"},
		Out: &out, Err: &bytes.Buffer{},
	}
	if err := (&DevicesListCmd{Policy: "finance", Facts: []string{"serial_number"}}).Run(app); err != nil {
		t.Fatal(err)
	}
	recs := parseCSV(t, out.String())
	want := []string{"AGENT ID,SERIAL NUMBER,LOCATION", "d2,A-1,Acme / Finance", "d3,C-3,Acme / Finance / Laptops", "d4,D-4,Acme / Finance / Laptops"}
	if len(recs) != len(want) {
		t.Fatalf("got %v", recs)
	}
	for i, w := range want {
		if got := strings.Join(recs[i], ","); got != w {
			t.Errorf("row %d = %q, want %q", i, got, w)
		}
	}
}

func TestFactCellCSVKeepsLongValuesAndLeavesMissingEmpty(t *testing.T) {
	long := strings.Repeat("x", 80)
	d := addigy.Device{Facts: map[string]addigy.Fact{
		"long": {Value: long},
		"bad":  {ErrorMsg: "boom"},
		"n":    {Value: 42.0},
	}}
	for id, want := range map[string]string{"long": long, "bad": "", "missing": "", "n": "42"} {
		if got := factCell(d, id, true); got != want {
			t.Errorf("csv %s = %q, want %q", id, got, want)
		}
	}
	if got := factCell(d, "long", false); len([]rune(got)) != 60 {
		t.Errorf("table cells stay shortened, got %d runes", len([]rune(got)))
	}
	if got := factCell(d, "missing", false); got != "-" {
		t.Errorf("table placeholder = %q", got)
	}
	if got := factCell(d, "bad", false); got != "(error)" {
		t.Errorf("table error cell = %q", got)
	}
}

func TestDevicesGetCSVIsJustTheFactTable(t *testing.T) {
	ds := newDeviceServer(t)
	var out bytes.Buffer
	app := &App{
		Ctx: context.Background(),
		G:   &Globals{APIKey: "k", BaseURL: ds.URL + "/api/v2", Output: "csv"},
		Out: &out, Err: &bytes.Buffer{},
	}
	if err := (&DevicesGetCmd{Ref: "d3"}).Run(app); err != nil {
		t.Fatal(err)
	}
	recs := parseCSV(t, out.String())
	if strings.Join(recs[0], ",") != "FACT,TYPE,VALUE" {
		t.Errorf("header = %v", recs[0])
	}
	if strings.Contains(out.String(), "Agent ID:") {
		t.Errorf("no preamble expected in CSV:\n%s", out.String())
	}
	found := map[string]string{}
	for _, r := range recs[1:] {
		found[r[0]] = r[2]
	}
	if found["serial_number"] != "C-3" || found["policy_id"] != "fin-laptops" {
		t.Errorf("facts = %v", found)
	}
}
