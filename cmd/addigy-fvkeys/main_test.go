package main

import (
	"bytes"
	"context"
	"errors"
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
)

// Acme ─┬─ Finance ── Laptops
//
//	└─ Sales
const policiesFixture = `[
  {"policyId":"acme","name":"Acme","parent":null},
  {"policyId":"finance","name":"Finance","parent":"acme"},
  {"policyId":"sales","name":"Sales","parent":"acme"},
  {"policyId":"fin-laptops","name":"Laptops","parent":"finance"}
]`

func device(id, name, serial, udid, policy string) string {
	facts := fmt.Sprintf(`"device_name":{"type":"string","value":%q},"serial_number":{"type":"string","value":%q},"policy_id":{"type":"string","value":%q}`, name, serial, policy)
	if udid != "" {
		facts += fmt.Sprintf(`,"udid":{"type":"string","value":%q}`, udid)
	}
	return fmt.Sprintf(`{"agentid":%q,"facts":{%s}}`, id, facts)
}

var devicesFixture = "[" + strings.Join([]string{
	device("d1", "beta", "S1", "U-1", "finance"),
	device("d2", "Alpha", "S2", "U-2", "fin-laptops"),
	device("d3", "gamma", "S3", "U-3", "sales"),
	device("d4", "delta", "S4", "", "finance"), // no UDID
}, ",") + "]"

// keysFixture are made-up keys per UDID; created is Unix time.
var keysFixture = map[string]string{
	"U-1": `[{"id":"k1","username":"u","key":"OLDK-0000-0000-0000","created":1700000000,"is_mdm":false},
	         {"id":"k2","username":"u","key":"NEWK-1111-1111-1111","created":1760000000,"is_mdm":true}]`,
	"U-2": `[]`,
	"U-3": `[{"id":"k3","username":"u","key":"SALE-3333-3333-3333","created":1760000000,"is_mdm":true}]`,
}

type fakeAddigy struct {
	*httptest.Server
	mu      sync.Mutex
	cookies []string // Cookie header of every keys request
	keys    func(w http.ResponseWriter, udid string)
}

func newFakeAddigy(t *testing.T) *fakeAddigy {
	t.Helper()
	fa := &fakeAddigy{}
	fa.keys = func(w http.ResponseWriter, udid string) { io.WriteString(w, keysFixture[udid]) }
	fa.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/oa/policies/query":
			io.WriteString(w, policiesFixture)
		case "/api/v2/devices":
			fmt.Fprintf(w, `{"items":%s,"metadata":{"page":1,"page_count":1,"per_page":50,"result_count":4,"total":4}}`, devicesFixture)
		case keysPath:
			if r.Header.Get("x-api-key") != "" {
				t.Error("the API key was sent to the web app")
			}
			fa.mu.Lock()
			fa.cookies = append(fa.cookies, r.Header.Get("Cookie"))
			fa.mu.Unlock()
			fa.keys(w, r.URL.Query().Get("device_udid"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fa.Close)
	return fa
}

type result struct {
	out, errOut string
	err         error
	asked       bool // whether the token was asked for
}

func (fa *fakeAddigy) run(t *testing.T, cli CLI) result {
	t.Helper()
	return fa.runWithConfig(t, cli, "")
}

// runWithConfig runs with an addigyctl config file holding cfg (JSON), if
// set.
func (fa *fakeAddigy) runWithConfig(t *testing.T, cli CLI, cfg string) result {
	t.Helper()
	if cfg != "" {
		cli.ConfigFile = filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(cli.ConfigFile, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cli.APIKey = "k"
	cli.BaseURL = fa.URL + "/api/v2"
	cli.AppURL = fa.URL
	if cli.ConfigFile == "" {
		cli.ConfigFile = filepath.Join(t.TempDir(), "none.json")
	}
	var out, errOut bytes.Buffer
	var r result
	r.err = run(context.Background(), &cli, env{
		out: &out, err: &errOut,
		token: func() (string, error) { r.asked = true; return "tok", nil },
		now:   func() time.Time { return time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC) },
	})
	r.out, r.errOut = out.String(), errOut.String()
	return r
}

func readRows(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows(sheet)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestExportPolicyWithSubPolicies(t *testing.T) {
	fa := newFakeAddigy(t)
	dir := filepath.Join(t.TempDir(), "exports")
	r := fa.runWithConfig(t, CLI{Policy: "Finance", ExportDir: dir}, `{"timezone":"UTC","date_format":"yyyy-mm-dd hh:mm:ss"}`)
	if r.err != nil {
		t.Fatal(r.err)
	}
	path := filepath.Join(dir, "fvkeys-finance-2026-10-21.xlsx")
	if want := "Exported 2 keys for 3 devices (1 without a key, 1 failed) to " + path; !strings.Contains(r.out, want) {
		t.Errorf("output %q, want %q", r.out, want)
	}
	if !strings.Contains(r.errOut, "delta (S4) has no UDID") {
		t.Errorf("no warning about the device without a UDID:\n%s", r.errOut)
	}
	if strings.Contains(r.out+r.errOut, "NEWK") || strings.Contains(r.out+r.errOut, "OLDK") {
		t.Error("a key was printed")
	}

	// Devices by name; per device the newest key first, with its creation
	// time in the configured time zone and date format; a device without
	// keys (or without a UDID) gets one row with an empty key. Sales is not
	// in Finance.
	want := [][]string{
		{"Device Name", "Serial Number", "Policy", "Created", "Key"},
		{"Alpha", "S2", "Acme / Finance / Laptops"},
		{"beta", "S1", "Acme / Finance", "2025-10-09 08:53:20", "NEWK-1111-1111-1111"},
		{"beta", "S1", "Acme / Finance", "2023-11-14 22:13:20", "OLDK-0000-0000-0000"},
		{"delta", "S4", "Acme / Finance"},
	}
	if got := readRows(t, path); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows =\n%q\nwant\n%q", got, want)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", fi.Mode().Perm(), err)
	}

	// Only the session cookie goes to the web app.
	for _, c := range fa.cookies {
		if c != "prod_auth_token=tok" {
			t.Errorf("Cookie header = %q", c)
		}
	}
	if len(fa.cookies) != 2 {
		t.Errorf("%d keys requests, want 2 (Alpha and beta)", len(fa.cookies))
	}
}

func TestExportDirect(t *testing.T) {
	fa := newFakeAddigy(t)
	path := filepath.Join(t.TempDir(), "keys.xlsx")
	if r := fa.run(t, CLI{Policy: "Acme / Finance", Direct: true, File: path}); r.err != nil {
		t.Fatal(r.err)
	}
	rows := readRows(t, path)
	if len(rows) != 4 || rows[1][0] != "beta" || rows[3][0] != "delta" {
		t.Errorf("--direct rows = %q, want beta (2 keys) and delta", rows)
	}
}

func TestRejectedSessionStopsWithoutFile(t *testing.T) {
	for name, respond := range map[string]func(http.ResponseWriter){
		"401":      func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) },
		"403":      func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) },
		"redirect": func(w http.ResponseWriter) { w.Header().Set("Location", "/login"); w.WriteHeader(http.StatusFound) },
	} {
		t.Run(name, func(t *testing.T) {
			fa := newFakeAddigy(t)
			fa.keys = func(w http.ResponseWriter, _ string) { respond(w) }
			path := filepath.Join(t.TempDir(), "keys.xlsx")
			r := fa.run(t, CLI{Policy: "Finance", File: path})
			if !errors.Is(r.err, errSession) {
				t.Errorf("err = %v, want errSession", r.err)
			}
			if len(fa.cookies) != 1 {
				t.Errorf("%d keys requests, want to stop after the first", len(fa.cookies))
			}
			if _, err := os.Stat(path); err == nil {
				t.Error("a file was written")
			}
		})
	}
}

func TestOtherErrorsAreReportedPerDevice(t *testing.T) {
	fa := newFakeAddigy(t)
	fa.keys = func(w http.ResponseWriter, udid string) {
		switch udid {
		case "U-1":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "U-2":
			io.WriteString(w, `<html>SECRET-IN-BODY</html>`)
		}
	}
	path := filepath.Join(t.TempDir(), "keys.xlsx")
	r := fa.run(t, CLI{Policy: "Finance", File: path})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.out, "0 keys for 3 devices (0 without a key, 3 failed)") {
		t.Errorf("output %q", r.out)
	}
	if !strings.Contains(r.errOut, "beta (S1): Addigy returned 500") || !strings.Contains(r.errOut, "Alpha (S2): Addigy's response was not the expected list of keys") {
		t.Errorf("warnings:\n%s", r.errOut)
	}
	if strings.Contains(r.errOut, "SECRET-IN-BODY") {
		t.Error("a response body was printed")
	}
}

func TestTokenFromEnvironment(t *testing.T) {
	fa := newFakeAddigy(t)
	t.Setenv(tokenEnv, " prod_auth_token=fromenv; \n")
	r := fa.run(t, CLI{Policy: "Finance", File: filepath.Join(t.TempDir(), "k.xlsx")})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.asked {
		t.Error("asked for the token although it is set in the environment")
	}
	if fa.cookies[0] != "prod_auth_token=fromenv" {
		t.Errorf("Cookie header = %q", fa.cookies[0])
	}
}

func TestExistingFileIsRefusedBeforeAskingForTheToken(t *testing.T) {
	fa := newFakeAddigy(t)
	path := filepath.Join(t.TempDir(), "k.xlsx")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := fa.run(t, CLI{Policy: "Finance", File: path})
	if r.err == nil || !strings.Contains(r.err.Error(), "--force") {
		t.Errorf("err = %v, want a refusal mentioning --force", r.err)
	}
	if r.asked || len(fa.cookies) != 0 {
		t.Error("asked for the token or fetched keys")
	}
	if r := fa.run(t, CLI{Policy: "Finance", File: path, Force: true}); r.err != nil {
		t.Errorf("with --force: %v", r.err)
	}
}

func TestErrors(t *testing.T) {
	fa := newFakeAddigy(t)
	if r := fa.run(t, CLI{Policy: "Nope"}); r.err == nil || !strings.Contains(r.err.Error(), "Nope") {
		t.Errorf("unknown policy: err = %v", r.err)
	}
	if r := fa.run(t, CLI{Policy: "Laptops", Direct: true, ExportDir: t.TempDir()}); r.err != nil {
		t.Errorf("Laptops: %v", r.err)
	}
}

func TestCreatedIn(t *testing.T) {
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip(err)
	}
	want := time.Date(2025, 10, 10, 13, 0, 28, 0, ams)
	for _, created := range []float64{float64(want.Unix()), float64(want.UnixMilli())} {
		if got, ok := (fvKey{Created: created}).createdIn(ams).(time.Time); !ok || !got.Equal(want) || got.Hour() != 13 {
			t.Errorf("createdIn(%v) = %v, want %v", created, got, want)
		}
	}
	if got := (fvKey{}).createdIn(ams); got != nil {
		t.Errorf("no creation time: got %v, want nil", got)
	}
}

func TestCleanToken(t *testing.T) {
	for in, want := range map[string]string{
		"abc.DEF-123_x":            "abc.DEF-123_x",
		"  abc\n":                  "abc",
		"prod_auth_token=abc;":     "abc",
		"prod_auth_token=abc":      "abc",
		"eyJhbGciOi.eyJzdWIi.sig=": "eyJhbGciOi.eyJzdWIi.sig=",
	} {
		if got, err := cleanToken(in); err != nil || got != want {
			t.Errorf("cleanToken(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"abc; other=1", "a b", "abc\r\nX-Evil: 1"} {
		if _, err := cleanToken(bad); err == nil {
			t.Errorf("cleanToken(%q) accepted", bad)
		}
	}
}
