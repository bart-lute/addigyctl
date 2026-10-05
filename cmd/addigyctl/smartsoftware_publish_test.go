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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/swfolder"
)

const fixtureAirtame = `{"identifier":"Airtame-u1","instruction_id":"i15","base_identifier":"Airtame","version":"4.15.0",
	"installation_script":"VERSION=4.15.0\ninstaller -pkg \"/x/Airtame (${VERSION})/Airtame-${VERSION}.pkg\"",
	"remove_script":"rm -rf /Applications/Airtame.app","category":"General","priority":10,"run_on_success":true,
	"predefined_conditions":{"app_exists":{"enabled":true,"operator":"lt","version":"4.15.0"}},"profiles":[],
	"downloads":[{"id":"f15","filename":"Airtame-4.15.0.pkg"}],"label":"Custom Software - Airtame (4.15.0)"}`

// Uploaded files in the fake. IDs are UUIDs: other --file values are names.
const (
	idAirtame16  = "00000000-0000-0000-0000-000000000016"
	idAirtame16b = "00000000-0000-0000-0000-00000000016b"
	idExtra16    = "00000000-0000-0000-0000-0000000000e1"
)

// fakeAddigy serves one item's versions, uploaded files (query by loose name
// or MD5, GET by id), GET by instruction id, and new-version, which it
// records and adds as version "i-new".
type fakeAddigy struct {
	t        *testing.T
	mu       sync.Mutex
	versions []string
	files    []addigy.File
	created  []map[string]any // new-version request bodies
	path     string           // new-version request path
}

func (f *fakeAddigy) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/v2/oa/smart-software/query":
		fmt.Fprintf(w, `{"items":[%s],"metadata":{"page":1,"page_count":1,"per_page":100,"total":%d}}`, strings.Join(f.versions, ","), len(f.versions))
	case r.URL.Path == "/api/v2/oa/files/query":
		var q struct {
			SearchTerm string   `json:"search_term"`
			MD5        []string `json:"md5_hash"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		var match []addigy.File
		for _, file := range f.files {
			// Loose, like Addigy: the name only has to share a prefix.
			if q.SearchTerm != "" && !strings.HasPrefix(strings.ToLower(file.Filename), strings.ToLower(q.SearchTerm[:min(7, len(q.SearchTerm))])) {
				continue
			}
			if len(q.MD5) > 0 && !slices.Contains(q.MD5, file.MD5Hash) {
				continue
			}
			match = append(match, file)
		}
		b, _ := json.Marshal(match)
		fmt.Fprintf(w, `{"items":%s,"metadata":{"page":1,"page_count":1,"per_page":100,"total":%d}}`, b, len(match))
	case strings.HasPrefix(r.URL.Path, "/api/v2/oa/files/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/oa/files/")
		for _, file := range f.files {
			if file.ID == id {
				json.NewEncoder(w).Encode(file)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"File not found."}`)
	case r.URL.Path == "/api/v2/oa/policies/query":
		io.WriteString(w, `[{"policyId":"p1","orgid":"o1"}]`)
	case strings.HasSuffix(r.URL.Path, "/new-version"):
		f.path = r.URL.Path
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body)
		body["instruction_id"] = "i-new"
		b, _ := json.Marshal(body)
		f.versions = append(f.versions, string(b))
		w.Write(b)
	case strings.HasPrefix(r.URL.Path, "/api/v2/o/o1/smart-software/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/o/o1/smart-software/")
		for _, v := range f.versions {
			if strings.Contains(v, `"instruction_id":"`+id+`"`) {
				io.WriteString(w, v)
				return
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"message":"custom software not found"}`)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}
}

// setupPublish exports fixtureAirtame (with placeholders) into a software
// root and returns an app pointed at the fake.
func setupPublish(t *testing.T) (*fakeAddigy, *App, *bytes.Buffer) {
	t.Helper()
	fake := &fakeAddigy{t: t, versions: []string{fixtureAirtame}, files: []addigy.File{
		{ID: "f15", Filename: "Airtame-4.15.0.pkg", MD5Hash: "m15", Created: "2025-01-01T00:00:00Z"},
		{ID: idAirtame16, Filename: "Airtame-4.16.0.pkg", MD5Hash: "m16", Created: "2026-10-01T00:00:00Z"},
		{ID: idExtra16, Filename: "Airtame-extras-4.16.0.zip", MD5Hash: "mx", Created: "2026-10-01T00:00:00Z"},
	}}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2", SoftwareRoot: t.TempDir()},
		In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}}
	if err := (&SmartSoftwareExportCmd{Ref: "Airtame-u1", Placeholders: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	return fake, app, &out
}

func TestNewVersionPublishes(t *testing.T) {
	fake, app, out := setupPublish(t)
	// No --file: export made version_downloads ["Airtame-{{.Version}}.pkg"].
	cmd := &SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0", Yes: true}
	if err := cmd.Run(app); err != nil {
		t.Fatal(err)
	}
	if len(fake.created) != 1 {
		t.Fatalf("expected one new version, got %d", len(fake.created))
	}
	if fake.path != "/api/v2/o/o1/smart-software/i15/new-version" {
		t.Errorf("new-version should be called on the current version's instruction id, got %s", fake.path)
	}
	body := fake.created[0]
	if body["version"] != "4.16.0" || body["identifier"] != "Airtame-u1" || body["base_identifier"] != "Airtame" {
		t.Errorf("unexpected identity/version in body: %v", body)
	}
	if s, _ := body["installation_script"].(string); !strings.HasPrefix(s, "VERSION=4.16.0\n") {
		t.Errorf("install script not rendered: %q", s)
	}
	ae := body["predefined_conditions"].(map[string]any)["app_exists"].(map[string]any)
	if ae["version"] != "4.16.0" {
		t.Errorf("app_exists.version = %v", ae["version"])
	}
	if dl, _ := json.Marshal(body["downloads"]); string(dl) != `[{"id":"`+idAirtame16+`"}]` {
		t.Errorf("downloads = %s; only the new version's file, not the old installer", dl)
	}
	for _, k := range []string{"label", "name", "provider", "organization_id"} {
		if _, ok := body[k]; ok {
			t.Errorf("server-managed field %q sent", k)
		}
	}
	if !strings.Contains(out.String(), "Created Airtame 4.16.0 (instruction_id i-new)") {
		t.Errorf("output:\n%s", out.String())
	}

	// The state now points at the new version, so the next publish has no drift.
	st, err := swfolder.ReadState(filepath.Join(app.G.SoftwareRoot, "airtame"))
	if err != nil || st.InstructionID != "i-new" || st.Version != "4.16.0" {
		t.Errorf("state after publish = %+v, %v", st, err)
	}
	fake.files = append(fake.files, addigy.File{ID: "f17", Filename: "Airtame-4.17.0.pkg", MD5Hash: "m17"})
	if err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.17.0", DryRun: true}).Run(app); err != nil {
		t.Errorf("a publish right after a publish should see no drift: %v", err)
	}
}

func TestNewVersionRefusesDrift(t *testing.T) {
	fake, app, _ := setupPublish(t)
	// Someone edits 4.15.0 in the Addigy UI.
	fake.versions[0] = strings.Replace(fake.versions[0], `"priority":10`, `"priority":3`, 1)

	err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0", Yes: true}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "edited in Addigy") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected a drift refusal, got %v", err)
	}
	if len(fake.created) != 0 {
		t.Fatal("nothing may be created when refusing")
	}
	if err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0", Yes: true, Force: true}).Run(app); err != nil {
		t.Errorf("--force should publish: %v", err)
	}
}

func TestNewVersionFolderChangesAreNotDrift(t *testing.T) {
	_, app, out := setupPublish(t)
	dir := filepath.Join(app.G.SoftwareRoot, "airtame")
	f, err := swfolder.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.RemoveScript = "rm -rf /Applications/Airtame.app\nrm -rf ~/Library/Airtame\n"
	if err := swfolder.Write(dir, f, "", true); err != nil {
		t.Fatal(err)
	}
	if err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0", DryRun: true}).Run(app); err != nil {
		t.Fatalf("a change in the folder is not drift: %v", err)
	}
	if !strings.Contains(out.String(), "+rm -rf ~/Library/Airtame") {
		t.Errorf("preview should show the folder's change:\n%s", out.String())
	}
}

func TestNewVersionDryRunAndConfirm(t *testing.T) {
	fake, app, out := setupPublish(t)
	if err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0", DryRun: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Dry run") || !strings.Contains(out.String(), "-VERSION=4.15.0\n  +VERSION=4.16.0") {
		t.Errorf("dry-run output:\n%s", out.String())
	}

	app.In = strings.NewReader("n\n")
	if err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0"}).Run(app); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("answering no should cancel, got %v", err)
	}
	if len(fake.created) != 0 {
		t.Fatal("a dry run or a cancelled confirmation must not create anything")
	}
	app.In = strings.NewReader("y\n")
	if err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0"}).Run(app); err != nil || len(fake.created) != 1 {
		t.Errorf("answering yes should create it: %v, %d created", err, len(fake.created))
	}
}

func TestNewVersionRefusals(t *testing.T) {
	fake, app, _ := setupPublish(t)
	for name, c := range map[string]struct {
		cmd  SmartSoftwareNewVersionCmd
		want string
	}{
		"existing version": {SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.15.0", Yes: true}, "already exists"},
		"lower version":    {SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.9", Yes: true}, "not higher"},
		"unknown file":     {SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0", File: []string{"nope"}, Yes: true}, "--file nope"},
		"not uploaded":     {SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.17.0", Yes: true}, `no file named "Airtame-4.17.0.pkg"`},
		"no folder":        {SmartSoftwareNewVersionCmd{Name: "Zoom", Version: "1.0", Yes: true}, "item.yaml"},
		"no target":        {SmartSoftwareNewVersionCmd{Version: "1.0", Yes: true}, "item's name"},
	} {
		if err := c.cmd.Run(app); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, c.want)
		}
	}
	if len(fake.created) != 0 {
		t.Fatal("nothing may be created when refusing")
	}
}

func TestConfirmRefusesWithoutTerminal(t *testing.T) {
	// A pipe is not a terminal: never prompt (and hang) in a script.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	app := &App{In: r, Err: &bytes.Buffer{}}
	if _, err := app.confirm("Go?"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("expected a refusal mentioning --yes, got %v", err)
	}
}

func TestNewVersionExportMadeAPattern(t *testing.T) {
	_, app, _ := setupPublish(t)
	f, err := swfolder.Read(filepath.Join(app.G.SoftwareRoot, "airtame"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.VersionDownloads) != 1 || f.VersionDownloads[0] != "Airtame-{{.Version}}.pkg" {
		t.Errorf("version_downloads = %q", f.VersionDownloads)
	}
}

// publishBody publishes 4.16.0 with the folder's version_downloads set to
// patterns, and returns the request's downloads.
func publishBody(t *testing.T, fake *fakeAddigy, app *App, patterns []string, files ...string) (string, error) {
	t.Helper()
	dir := filepath.Join(app.G.SoftwareRoot, "airtame")
	f, err := swfolder.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.VersionDownloads = patterns
	if err := swfolder.Write(dir, f, "", true); err != nil {
		t.Fatal(err)
	}
	if err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0", File: files, Yes: true}).Run(app); err != nil {
		return "", err
	}
	dl, _ := json.Marshal(fake.created[len(fake.created)-1]["downloads"])
	return string(dl), nil
}

func TestNewVersionDownloadCounts(t *testing.T) {
	// One file in 4.15.0; 4.16.0 can have two, or none.
	fake, app, _ := setupPublish(t)
	dl, err := publishBody(t, fake, app, []string{"Airtame-{{.Version}}.pkg", "Airtame-extras-{{.Version}}.zip"})
	if err != nil || dl != `[{"id":"`+idAirtame16+`"},{"id":"`+idExtra16+`"}]` {
		t.Errorf("two files: %s, %v", dl, err)
	}

	// Going from one file to none is refused: usually a forgotten pattern.
	fake, app, _ = setupPublish(t)
	_, err = publishBody(t, fake, app, []string{})
	if err == nil || !strings.Contains(err.Error(), "would have none") {
		t.Fatalf("expected a refusal to drop every download, got %v", err)
	}
	if len(fake.created) != 0 {
		t.Fatal("nothing may be created when refusing")
	}
	// --force publishes it anyway.
	if err := (&SmartSoftwareNewVersionCmd{Name: "Airtame", Version: "4.16.0", Yes: true, Force: true}).Run(app); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if dl, _ := json.Marshal(fake.created[0]["downloads"]); string(dl) != `[]` {
		t.Errorf("no files with --force: %s", dl)
	}
}

func TestNewVersionDuplicateNames(t *testing.T) {
	// Same name, different content: refuse and list them.
	fake, app, _ := setupPublish(t)
	fake.files = append(fake.files, addigy.File{ID: idAirtame16b, Filename: "Airtame-4.16.0.pkg", MD5Hash: "other", Created: "2026-10-02T00:00:00Z"})
	_, err := publishBody(t, fake, app, []string{"Airtame-{{.Version}}.pkg"})
	if err == nil || !strings.Contains(err.Error(), "2 different files") || !strings.Contains(err.Error(), idAirtame16b) {
		t.Fatalf("expected a refusal listing both uploads, got %v", err)
	}
	// --file <id> picks one.
	dl, err := publishBody(t, fake, app, []string{"Airtame-{{.Version}}.pkg"}, idAirtame16b)
	if err != nil || dl != `[{"id":"`+idAirtame16b+`"}]` {
		t.Errorf("--file id: %s, %v", dl, err)
	}

	// Same name, same content: harmless, the newest is used.
	fake, app, out := setupPublish(t)
	fake.files = append(fake.files, addigy.File{ID: idAirtame16b, Filename: "Airtame-4.16.0.pkg", MD5Hash: "m16", Created: "2026-10-02T00:00:00Z"})
	dl, err = publishBody(t, fake, app, []string{"Airtame-{{.Version}}.pkg"})
	if err != nil || dl != `[{"id":"`+idAirtame16b+`"}]` {
		t.Errorf("identical duplicates: %s, %v", dl, err)
	}
	if !strings.Contains(out.String(), "uploaded 2 times with the same content") {
		t.Errorf("expected a note about the duplicate:\n%s", out.String())
	}
}

func TestNewVersionFileByLocalPath(t *testing.T) {
	fake, app, _ := setupPublish(t)
	local := filepath.Join(t.TempDir(), "Airtame-4.16.0.pkg")
	if err := os.WriteFile(local, []byte("the installer"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := md5File(local)
	fake.files = append(fake.files, addigy.File{ID: idAirtame16b, Filename: "Airtame-4.16.0.pkg", MD5Hash: sum, Created: "2026-09-01T00:00:00Z"})
	dl, err := publishBody(t, fake, app, []string{"Airtame-{{.Version}}.pkg"}, local)
	if err != nil || dl != `[{"id":"`+idAirtame16b+`"}]` {
		t.Errorf("--file <local path> should pick the upload with the same content: %s, %v", dl, err)
	}
}
