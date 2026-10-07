package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ginkio/addigyctl/internal/addigy"
)

// setupFiles serves three files over two pages, and usages for two of them:
// one Smart Software version, and a policy Addigy has no name for.
func setupFiles(t *testing.T, format string) (*App, *bytes.Buffer, *[]string) {
	t.Helper()
	var asked []string // file IDs sent to /files/usage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/oa/files/query":
			var q struct{ Page int }
			json.NewDecoder(r.Body).Decode(&q)
			items := map[int]string{
				1: `{"id":"f1","filename":"helloworld-1.2.0","size":2429714,"created":"2026-10-05T13:57:36Z"},
				    {"id":"f2","filename":"Canon.icns","size":120000,"created":"2017-09-20T10:00:00Z"}`,
				2: `{"id":"f3-` + strings.Repeat("x", 100) + `","filename":"wifi.mobileconfig","size":900,"created":"2024-01-01T00:00:00Z"}`,
			}[q.Page]
			fmt.Fprintf(w, `{"items":[%s],"metadata":{"page":%d,"page_count":2,"per_page":2,"total":3}}`, items, q.Page)
		case "/api/v2/files/usage":
			var q struct {
				FileIDs []string `json:"file_ids"`
			}
			json.NewDecoder(r.Body).Decode(&q)
			asked = q.FileIDs
			io.WriteString(w, `[
				{"file_id":"f1","feature_type":"ansible-custom-software","feature_name":"instruction","item_id":"i12","item_name":"Hello World (1.2.0)"},
				{"file_id":"f3-`+strings.Repeat("x", 100)+`","feature_type":"policy","feature_name":"identity-configuration","item_id":"p1","item_name":"Not available"}]`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2", Output: format},
		Out: &out, Err: &bytes.Buffer{}}
	return app, &out, &asked
}

func TestFilesList(t *testing.T) {
	app, out, asked := setupFiles(t, "")
	if err := (&FilesListCmd{}).Run(app); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 3 || (*asked)[0] != "f1" || !strings.HasPrefix((*asked)[2], "f3-") {
		t.Errorf("usage asked for %v; want every page's files", *asked)
	}
	got := out.String()
	for _, want := range []string{"Hello World (1.2.0)", "policy p1", "2.4 MB", "3 files, 1 unused (120.0 KB)", "f3-" + strings.Repeat("x", 32) + "…"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	// Newest first by default.
	if i, j := strings.Index(got, "helloworld"), strings.Index(got, "Canon"); i > j {
		t.Errorf("not newest first:\n%s", got)
	}
}

func TestFilesListUnused(t *testing.T) {
	app, out, _ := setupFiles(t, "json")
	if err := (&FilesListCmd{Unused: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	var got []fileUses
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "f2" {
		t.Fatalf("unused = %+v", got)
	}
	if !strings.Contains(out.String(), `"usages": []`) {
		t.Errorf("an unused file should have usages [], not null:\n%s", out)
	}
}

func TestFilesListCSV(t *testing.T) {
	app, out, _ := setupFiles(t, "csv")
	if err := (&FilesListCmd{Name: "HELLO"}).Run(app); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "f1,helloworld-1.2.0,2429714,") {
		t.Errorf("csv (exact bytes, --name filtered):\n%s", out)
	}
}

func TestSortFileUses(t *testing.T) {
	files := []fileUses{
		{File: addigy.File{Filename: "b", Size: 5}},
		{File: addigy.File{Filename: "a", Size: 5}},
		{File: addigy.File{Filename: "c", Size: 9}, Usages: []addigy.FileUsage{{}, {}}},
	}
	names := func() string {
		var s string
		for _, f := range files {
			s += f.Filename
		}
		return s
	}
	for _, c := range []struct {
		col  string
		desc bool
		want string
	}{
		{"size", true, "cab"}, // largest first; ties A-Z
		{"size", false, "abc"},
		{"uses", true, "cab"},
		{"name", false, "abc"},
	} {
		sortFileUses(files, c.col, c.desc)
		if got := names(); got != c.want {
			t.Errorf("sort %s desc=%v = %s, want %s", c.col, c.desc, got, c.want)
		}
	}
	if err := (&FilesListCmd{Sort: "md5"}).Run(&App{}); err == nil || !strings.Contains(err.Error(), "unknown --sort") {
		t.Errorf("bad --sort: %v", err)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 KB", 2429714: "2.4 MB", 21_790_000_000: "21.8 GB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFilesListUnusedTable(t *testing.T) {
	app, out, _ := setupFiles(t, "")
	if err := (&FilesListCmd{Unused: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "USES") || strings.Contains(got, "USED BY") || !strings.Contains(got, "Canon.icns") {
		t.Errorf("--unused table should drop the usage columns:\n%s", got)
	}
	app, out, _ = setupFiles(t, "csv")
	if err := (&FilesListCmd{Unused: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "ID,FILENAME,SIZE,CREATED,USES,USED BY\n") {
		t.Errorf("--unused CSV should keep every column:\n%s", out)
	}
}
