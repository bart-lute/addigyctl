package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ginkio/addigyctl/internal/swfolder"
)

func TestCmpVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"9.2.2.898", "10.0.0.1030", -1},
		{"4.9", "4.10", -1},
		{"2505.058", "2505.1016", -1},
		{"1.0", "1.0.1", -1},
		{"1.0", "1.0", 0},
		{"6.1.0", "6.0.12", 1},
		{"1.0b2", "1.0b10", -1},
		{"", "1.0", -1},
	} {
		if got := cmpVersion(c.a, c.b); got != c.want {
			t.Errorf("cmpVersion(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestItemTargetPath(t *testing.T) {
	app := &App{G: &Globals{SoftwareRoot: "/sw"}}
	if p, err := (itemTarget{Item: "zoom"}).path(app); err != nil || p != "/sw/zoom" {
		t.Errorf("--item zoom = %q, %v", p, err)
	}
	if p, err := (itemTarget{Dir: "/elsewhere/zoom"}).path(app); err != nil || p != "/elsewhere/zoom" {
		t.Errorf("--dir = %q, %v", p, err)
	}
	for _, bad := range []string{"a/b", "..", "."} {
		if _, err := (itemTarget{Item: bad}).path(app); err == nil {
			t.Errorf("--item %q should be rejected", bad)
		}
	}
	if _, err := (itemTarget{Item: "zoom"}).path(&App{G: &Globals{}}); err == nil || !strings.Contains(err.Error(), "software root") {
		t.Errorf("--item without a software root: got %v", err)
	}
	if _, err := (itemTarget{}).path(app); err == nil {
		t.Error("neither --dir nor --item should be an error")
	}
	if p, err := (itemTarget{}).pathOr(app, "zoom"); err != nil || p != "/sw/zoom" {
		t.Errorf("default folder = %q, %v", p, err)
	}
	if p, err := (itemTarget{Item: "mine"}).pathOr(app, "zoom"); err != nil || p != "/sw/mine" {
		t.Errorf("--item should override the default folder: %q, %v", p, err)
	}
	if _, err := (itemTarget{}).pathOr(app, ""); err == nil {
		t.Error("an unusable default folder name should be an error")
	}
}

// newSmartSoftwareServer serves the smart-software query endpoint: items
// whose identifier matches the request's identifier filter, or, without one,
// items whose base_identifier contains name_contains.
func newSmartSoftwareServer(t *testing.T, items ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/oa/smart-software/query" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var body struct {
			Query struct {
				Identifier   string `json:"identifier"`
				NameContains string `json:"name_contains"`
			} `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var match []string
		for _, it := range items {
			var s struct{ Identifier, BaseIdentifier string }
			json.Unmarshal([]byte(strings.ReplaceAll(it, "base_identifier", "baseidentifier")), &s)
			switch {
			case body.Query.Identifier != "":
				if s.Identifier == body.Query.Identifier {
					match = append(match, it)
				}
			case strings.Contains(strings.ToLower(s.BaseIdentifier), strings.ToLower(body.Query.NameContains)):
				match = append(match, it)
			}
		}
		fmt.Fprintf(w, `{"items":[%s],"metadata":{"page":1,"page_count":1,"per_page":100,"result_count":%d,"total":%d}}`,
			strings.Join(match, ","), len(match), len(match))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const (
	fixtureZoomOld = `{"identifier":"Zoom-u1","instruction_id":"i1","base_identifier":"Zoom","version":"9.2","category":"Video",
		"installation_script":"old","downloads":[{"id":"f1","filename":"zoom-9.2.pkg"}]}`
	fixtureZoomNew = `{"identifier":"Zoom-u1","instruction_id":"i2","base_identifier":"Zoom","version":"10.0","category":"Video",
		"priority":5,"run_on_success":true,"installation_script":"install {{.Version}}","condition":"",
		"remove_script":"rm -rf /Applications/zoom.us.app","label":"server-managed","provider":"ansible-custom-software",
		"predefined_conditions":{"app_exists":{"enabled":true,"path":"/Applications/zoom.us.app"}},
		"software_icon":{"id":"https://example.com/zoom.png","provider":"web","filename":"zoom.png"},
		"downloads":[{"id":"f2","filename":"zoom-10.0.pkg"}]}`
	fixtureZoomRooms = `{"identifier":"Zoom Rooms-u9","instruction_id":"i9","base_identifier":"Zoom Rooms","version":"1.0"}`
)

func TestSmartSoftwareExportByName(t *testing.T) {
	srv := newSmartSoftwareServer(t, fixtureZoomOld, fixtureZoomNew, fixtureZoomRooms)
	root := t.TempDir()
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2", SoftwareRoot: root}, Out: &out, Err: &bytes.Buffer{}}

	// "zoom" also matches "Zoom Rooms" by name_contains; only the exact name
	// counts. Without --dir or --item, the folder is named after the item.
	cmd := &SmartSoftwareExportCmd{Ref: "zoom"}
	if err := cmd.Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Exported Zoom 10.0") {
		t.Errorf("expected the latest version (10.0, not 9.2) to be exported:\n%s", out.String())
	}

	dir := filepath.Join(root, "zoom")
	f, err := swfolder.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.Identifier != "Zoom-u1" || f.BaseIdentifier != "Zoom" || f.Priority != 5 || !f.RunOnSuccess {
		t.Errorf("item = %+v", f.Item)
	}
	// Braces in Addigy's content are literal text, so export escapes them.
	if f.InstallScript != "install {{\"{{\"}}.Version}}\n" || f.Condition != "" || f.RemoveScript == "" {
		t.Errorf("scripts = %q / %q / %q", f.InstallScript, f.Condition, f.RemoveScript)
	}
	if len(f.Downloads) != 0 {
		t.Errorf("the version's downloads must not be copied into item.yaml: %+v", f.Downloads)
	}
	if f.SoftwareIcon == nil || f.SoftwareIcon.ID != "https://example.com/zoom.png" {
		t.Errorf("icon = %+v", f.SoftwareIcon)
	}
	data, _ := os.ReadFile(filepath.Join(dir, swfolder.ItemFile))
	for _, want := range []string{"version 10.0 (instruction_id i2)", "f2  zoom-10.0.pkg"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("item.yaml header missing %q:\n%s", want, data)
		}
	}
	for _, unwanted := range []string{"label", "provider: ansible", "server-managed"} {
		if strings.Contains(string(data), unwanted) {
			t.Errorf("item.yaml has server-managed field %q:\n%s", unwanted, data)
		}
	}

	// A second export refuses to overwrite; --force does.
	if err := cmd.Run(app); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("expected a refusal, got %v", err)
	}
	cmd.Force = true
	if err := cmd.Run(app); err != nil {
		t.Errorf("--force: %v", err)
	}
}

func TestSmartSoftwareResolveAmbiguousName(t *testing.T) {
	other := strings.ReplaceAll(fixtureZoomRooms, `"Zoom Rooms-u9"`, `"Zoom-u7"`)
	other = strings.ReplaceAll(other, `"Zoom Rooms"`, `"zoom"`)
	srv := newSmartSoftwareServer(t, fixtureZoomNew, other)
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2"}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	_, err := resolveSmartSoftware(app, "Zoom")
	if err == nil || !strings.Contains(err.Error(), "Zoom-u1") || !strings.Contains(err.Error(), "Zoom-u7") {
		t.Errorf("expected an ambiguity error listing both identifiers, got %v", err)
	}
}

func TestSmartSoftwareResolveByIdentifier(t *testing.T) {
	srv := newSmartSoftwareServer(t, fixtureZoomOld, fixtureZoomNew)
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2"}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	s, err := resolveSmartSoftware(app, "Zoom-u1")
	if err != nil || s.InstructionID != "i2" {
		t.Errorf("got %+v, %v; want the latest version i2", s, err)
	}
}

func TestSmartSoftwareExportPlaceholders(t *testing.T) {
	item := `{"identifier":"Airtame-u1","instruction_id":"i1","base_identifier":"Airtame","version":"4.15.0",
		"installation_script":"VERSION=4.15.0\necho {{literal}}",
		"predefined_conditions":{"app_exists":{"enabled":true,"operator":"lt","version":"4.15.0"}}}`
	srv := newSmartSoftwareServer(t, item)
	root := t.TempDir()
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2", SoftwareRoot: root}, Out: &out, Err: &bytes.Buffer{}}
	if err := (&SmartSoftwareExportCmd{Ref: "Airtame", Placeholders: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "item.yaml (1), install.sh (1)") {
		t.Errorf("expected per-file replacement counts:\n%s", out.String())
	}

	f, err := swfolder.Read(filepath.Join(root, "airtame"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.InstallScript, "VERSION={{.Version}}\n") {
		t.Errorf("install script = %q", f.InstallScript)
	}
	// Rendering for a new version updates the version and keeps literal braces.
	r, err := f.Render(swfolder.Vars{Version: "4.16.0"})
	if err != nil {
		t.Fatal(err)
	}
	if r.InstallScript != "VERSION=4.16.0\necho {{literal}}\n" {
		t.Errorf("rendered install script = %q", r.InstallScript)
	}
	if v := r.PredefinedConditions["app_exists"].(map[string]any)["version"]; v != "4.16.0" {
		t.Errorf("rendered app_exists.version = %v", v)
	}
}
