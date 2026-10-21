package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/ginkio/addigyctl/internal/xlsx"
)

const exportFactsFixture = `[
  {"identifier":"serial_number","name":"Serial Number","return_type":"string"},
  {"identifier":"device_name","name":"Device Name","return_type":"string"},
  {"identifier":"policy_id","name":"Policy ID","return_type":"string"},
  {"identifier":"online","name":"Online","return_type":"boolean"},
  {"identifier":"last_online","name":"Last Online","return_type":"date"},
  {"identifier":"battery_cycles","name":"Battery Cycles","return_type":"number"},
  {"identifier":"admin_users","name":"admin users","return_type":"list"}
]`

// exportDevicesFixture uses the policy hierarchy of policiesFixture.
const exportDevicesFixture = `[
  {"agentid":"d1","facts":{
    "device_name":{"type":"string","value":"beta"},
    "serial_number":{"type":"string","value":"S1"},
    "policy_id":{"type":"string","value":"finance"},
    "online":{"type":"boolean","value":true},
    "last_online":{"type":"datetime","value":"2026-03-04T10:20:30Z"},
    "battery_cycles":{"type":"number","value":123},
    "admin_users":{"type":"list","value":["admin","helpdesk"]}}},
  {"agentid":"d2","facts":{
    "device_name":{"type":"string","value":"Alpha"},
    "serial_number":{"type":"string","value":"S2"},
    "policy_id":{"type":"string","value":"fin-laptops"},
    "last_online":{"type":"datetime","value":"not a date"}}},
  {"agentid":"d3","facts":{
    "device_name":{"type":"string","value":"gamma"},
    "serial_number":{"type":"string","value":"S3"},
    "policy_id":{"type":"string","value":"sales"}}}
]`

type exportServer struct {
	*httptest.Server
	mu          sync.Mutex
	paths       []string
	deviceFacts []string // desired_fact_identifiers of the last device search
}

func newExportServer(t *testing.T) *exportServer {
	t.Helper()
	es := &exportServer{}
	es.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		es.mu.Lock()
		es.paths = append(es.paths, r.URL.Path)
		es.mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/o/org1/facts":
			io.WriteString(w, exportFactsFixture)
		case "/api/v2/oa/policies/query":
			io.WriteString(w, policiesFixture)
		case "/api/v2/devices":
			var body struct {
				DesiredFactIdentifiers []string `json:"desired_fact_identifiers"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("bad request body: %v", err)
			}
			es.mu.Lock()
			es.deviceFacts = body.DesiredFactIdentifiers
			es.mu.Unlock()
			fmt.Fprintf(w, `{"items":%s,"metadata":{"page":1,"page_count":1,"per_page":50,"result_count":3,"total":3}}`, exportDevicesFixture)
		case "/api/v2/static-fields/":
			io.WriteString(w, `{"items":[{"id":"f1","name":"asset_tag"}],"metadata":{"page":1,"page_count":1,"per_page":100,"result_count":1,"total":1}}`)
		case "/api/v2/static-fields/value":
			io.WriteString(w, `{"items":[{"agentid":"d2","static_field_id":"f1","value":"T-0042"}],"metadata":{"page":1,"page_count":1,"per_page":100,"result_count":1,"total":1}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(es.Close)
	return es
}

func (es *exportServer) run(t *testing.T, g Globals, cfgDateFormat string, cmd *DevicesExportCmd) runResult {
	t.Helper()
	var out, errOut bytes.Buffer
	g.APIKey, g.BaseURL, g.OrgID = "k", es.URL+"/api/v2", "org1"
	app := &App{Ctx: context.Background(), G: &g, Out: &out, Err: &errOut, loc: time.FixedZone("UTC+2", 2*3600)}
	app.Cfg.DateFormat = cfgDateFormat
	err := cmd.Run(app)
	return runResult{out.String(), errOut.String(), err}
}

func openExport(t *testing.T, path string) *excelize.File {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestDevicesExportPolicy(t *testing.T) {
	es := newExportServer(t)
	path := filepath.Join(t.TempDir(), "out.xlsx")
	r := es.run(t, Globals{}, "", &DevicesExportCmd{File: path, Policy: "Finance"})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if want := "Exported 2 devices (9 columns) to " + path; !strings.Contains(r.out, want) {
		t.Errorf("output %q, want %q", r.out, want)
	}

	f := openExport(t, path)
	rows, err := f.GetRows(exportSheet)
	if err != nil {
		t.Fatal(err)
	}
	// Lead columns, then the other facts and the static field by header,
	// case-insensitively. Rows by device name, case-insensitively; sales
	// (gamma) is outside Finance.
	wantHeader := []string{"Device Name", "Serial Number", "Policy", "admin users", "asset_tag", "Battery Cycles", "Last Online", "Online", "Policy ID"}
	if fmt.Sprint(rows[0]) != fmt.Sprint(wantHeader) {
		t.Errorf("header = %q\nwant     %q", rows[0], wantHeader)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want header + 2:\n%q", len(rows), rows)
	}
	if rows[1][0] != "Alpha" || rows[2][0] != "beta" {
		t.Errorf("rows not sorted by name: %q", rows)
	}
	if rows[1][2] != "Acme / Finance / Laptops" || rows[1][4] != "T-0042" || rows[1][6] != "not a date" {
		t.Errorf("Alpha row = %q", rows[1])
	}
	if rows[2][3] != "admin, helpdesk" || rows[2][4] != "" || rows[2][7] != "true" {
		t.Errorf("beta row = %q", rows[2])
	}

	// Numbers and timestamps are real Excel values; the timestamp is shown
	// in the configured time zone (UTC+2 here).
	if typ, _ := f.GetCellType(exportSheet, "F3"); typ != excelize.CellTypeNumber && typ != excelize.CellTypeUnset {
		t.Errorf("Battery Cycles cell type = %v, want a number", typ)
	}
	if v, _ := f.GetCellValue(exportSheet, "F3", excelize.Options{RawCellValue: true}); v != "123" {
		t.Errorf("Battery Cycles raw value = %q", v)
	}
	raw, _ := f.GetCellValue(exportSheet, "G3", excelize.Options{RawCellValue: true})
	if want := mustExcelTime(t, time.Date(2026, 3, 4, 12, 20, 30, 0, time.UTC)); raw != want {
		t.Errorf("Last Online raw value = %q, want %q", raw, want)
	}
	if st := cellStyle(t, f, "G3"); st.NumFmt != xlsx.DateTimeFormat {
		t.Errorf("Last Online number format = %d, want %d", st.NumFmt, xlsx.DateTimeFormat)
	}

	// Header: bold, light blue, frozen, with a filter over the whole table.
	st := cellStyle(t, f, "I1")
	if st.Font == nil || !st.Font.Bold || len(st.Fill.Color) != 1 || !strings.EqualFold(st.Fill.Color[0], xlsx.HeaderFill) {
		t.Errorf("header style = font %+v fill %+v", st.Font, st.Fill)
	}
	panes, err := f.GetPanes(exportSheet)
	if err != nil {
		t.Fatal(err)
	}
	if !panes.Freeze || panes.YSplit != 1 || panes.TopLeftCell != "A2" {
		t.Errorf("panes = %+v", panes)
	}
	var filter string
	for _, dn := range f.GetDefinedName() {
		if dn.Name == "_xlnm._FilterDatabase" {
			filter = dn.RefersTo
		}
	}
	if !strings.HasSuffix(filter, "$A$1:$I$3") {
		t.Errorf("autofilter = %q", filter)
	}

	// Only the current user can read the export.
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", fi.Mode().Perm(), err)
	}
	// Every fact is fetched, the policy (location) included.
	for _, id := range []string{"device_name", "serial_number", "policy_id", "admin_users"} {
		if !contains(es.deviceFacts, id) {
			t.Errorf("device search did not ask for %s: %v", id, es.deviceFacts)
		}
	}
}

func TestDevicesExportDateFormatFromConfig(t *testing.T) {
	es := newExportServer(t)
	path := filepath.Join(t.TempDir(), "out.xlsx")
	if r := es.run(t, Globals{}, "dd-mm-yyyy hh:mm", &DevicesExportCmd{File: path}); r.err != nil {
		t.Fatal(r.err)
	}
	f := openExport(t, path)
	// Without --policy every device is exported: Alpha, beta, gamma.
	if v, _ := f.GetCellValue(exportSheet, "A4"); v != "gamma" {
		t.Errorf("A4 = %q, want gamma", v)
	}
	st := cellStyle(t, f, "G3")
	if st.CustomNumFmt == nil || *st.CustomNumFmt != "dd-mm-yyyy hh:mm" {
		t.Errorf("custom number format = %v", st.CustomNumFmt)
	}
	if v, _ := f.GetCellValue(exportSheet, "G3"); v != "04-03-2026 12:20" {
		t.Errorf("formatted Last Online = %q", v)
	}
}

func TestDevicesExportFactsKeepOrderWithoutStaticFields(t *testing.T) {
	es := newExportServer(t)
	path := filepath.Join(t.TempDir(), "out.xlsx")
	r := es.run(t, Globals{}, "", &DevicesExportCmd{File: path, Facts: []string{"online", "battery_cycles"}})
	if r.err != nil {
		t.Fatal(r.err)
	}
	f := openExport(t, path)
	rows, _ := f.GetRows(exportSheet)
	want := []string{"Device Name", "Serial Number", "Policy", "Online", "Battery Cycles"}
	if fmt.Sprint(rows[0]) != fmt.Sprint(want) {
		t.Errorf("header = %q, want %q", rows[0], want)
	}
	for _, p := range es.paths {
		if strings.Contains(p, "static-fields") {
			t.Errorf("static fields fetched with --fact: %s", p)
		}
	}
}

func TestDevicesExportDefaultFileNameAndOverwrite(t *testing.T) {
	es := newExportServer(t)
	dir := filepath.Join(t.TempDir(), "exports") // created by the export
	g := Globals{ExportDir: dir}
	cmd := &DevicesExportCmd{Policy: "Acme / Finance", Direct: true}
	r := es.run(t, g, "", cmd)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("export folder mode = %v, %v", fi.Mode().Perm(), err)
	}
	name := filepath.Join(dir, "devices-finance-"+time.Now().In(time.FixedZone("UTC+2", 2*3600)).Format("2006-01-02")+".xlsx")
	f := openExport(t, name)
	rows, _ := f.GetRows(exportSheet)
	if len(rows) != 2 || rows[1][0] != "beta" {
		t.Errorf("--direct rows = %q, want only beta", rows)
	}

	if r := es.run(t, g, "", cmd); r.err == nil || !strings.Contains(r.err.Error(), "--force") {
		t.Errorf("second export: err = %v, want a refusal mentioning --force", r.err)
	}
	cmd.Force = true
	if r := es.run(t, g, "", cmd); r.err != nil {
		t.Errorf("with --force: %v", r.err)
	}
}

func TestDevicesExportErrors(t *testing.T) {
	es := newExportServer(t)
	path := filepath.Join(t.TempDir(), "out.xlsx")
	for _, tc := range []struct {
		name string
		g    Globals
		cmd  DevicesExportCmd
		want string
	}{
		{"unknown fact", Globals{}, DevicesExportCmd{File: path, Facts: []string{"online", "nope"}}, "unknown fact identifier(s): nope"},
		{"direct without policy", Globals{}, DevicesExportCmd{File: path, Direct: true}, "--direct"},
		{"json", Globals{JSON: true}, DevicesExportCmd{File: path}, "Excel"},
		{"csv", Globals{Output: "csv"}, DevicesExportCmd{File: path}, "Excel"},
		{"unknown policy", Globals{}, DevicesExportCmd{File: path, Policy: "Nope"}, "Nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.cmd
			r := es.run(t, tc.g, "", &cmd)
			if r.err == nil || !strings.Contains(r.err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", r.err, tc.want)
			}
		})
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a failed export left a file behind")
	}
}

func cellStyle(t *testing.T, f *excelize.File, cell string) *excelize.Style {
	t.Helper()
	id, err := f.GetCellStyle(exportSheet, cell)
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.GetStyle(id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// mustExcelTime renders t the way excelize stores a time as a raw cell value.
func mustExcelTime(t *testing.T, tm time.Time) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetCellValue("Sheet1", "A1", tm); err != nil {
		t.Fatal(err)
	}
	v, err := f.GetCellValue("Sheet1", "A1", excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
