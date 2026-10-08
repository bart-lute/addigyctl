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
	"testing"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
)

// restoreFake serves an item's remaining versions, uploaded files (GET by
// id, query by MD5), and records create and new-version requests.
type restoreFake struct {
	versions    []string // JSON of the versions Addigy still has
	files       []addigy.File
	created     []map[string]any // create request bodies
	newVersions []map[string]any // new-version request bodies
	nvPaths     []string         // new-version request paths
	unarchive   bool             // store restored versions as not archived
	n           int
}

func (f *restoreFake) serve(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const sw = "/api/v2/o/o1/smart-software"
		switch {
		case r.URL.Path == "/api/v2/oa/policies/query":
			io.WriteString(w, `[{"policyId":"p1","orgid":"o1"}]`)
		case r.URL.Path == "/api/v2/oa/smart-software/query":
			var q struct {
				Query struct{ Identifier string }
			}
			json.NewDecoder(r.Body).Decode(&q)
			var match []string
			for _, v := range f.versions {
				if q.Query.Identifier == "" || strings.Contains(v, `"identifier":"`+q.Query.Identifier+`"`) {
					match = append(match, v)
				}
			}
			fmt.Fprintf(w, `{"items":[%s],"metadata":{"page":1,"page_count":1,"per_page":100,"total":%d}}`, strings.Join(match, ","), len(match))
		case r.URL.Path == "/api/v2/oa/files/query":
			var q struct {
				MD5 []string `json:"md5_hash"`
			}
			json.NewDecoder(r.Body).Decode(&q)
			var match []addigy.File
			for _, file := range f.files {
				if len(q.MD5) == 0 || file.MD5Hash == q.MD5[0] {
					match = append(match, file)
				}
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
		case r.Method == http.MethodPost && (r.URL.Path == sw || strings.HasSuffix(r.URL.Path, "/new-version")):
			var req, body map[string]any
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &req)
			json.Unmarshal(raw, &body) // a copy to store, so req stays as sent
			if r.URL.Path == sw {
				f.created = append(f.created, req)
				body["identifier"] = fmt.Sprintf("%v-new%d", body["base_identifier"], len(f.created))
			} else {
				f.newVersions = append(f.newVersions, req)
				f.nvPaths = append(f.nvPaths, r.URL.Path)
			}
			f.n++
			body["instruction_id"] = fmt.Sprintf("r%d", f.n)
			if f.unarchive {
				body["archived"] = false
			}
			b, _ := json.Marshal(body)
			f.versions = append(f.versions, string(b))
			w.Write(b)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, sw+"/"):
			id := strings.TrimPrefix(r.URL.Path, sw+"/")
			for _, v := range f.versions {
				if strings.Contains(v, `"instruction_id":"`+id+`"`) {
					io.WriteString(w, v)
					return
				}
			}
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"message":"custom software not found"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}
}

// writeTestBackup writes a backup of Xelion <version> with one download and
// an uploaded icon, as smart-software delete would, and returns its path.
func writeTestBackup(t *testing.T, dir, org, version string) string {
	t.Helper()
	return writeTestBackupAt(t, dir, org, version, time.Now())
}

// backupID is the deleted version's instruction ID in writeTestBackup's
// backups: 9.7.0.0 -> aaaaaaaa-0000-0000-0000-000000009700.
func backupID(version string) string {
	return fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012s", strings.ReplaceAll(version, ".", ""))
}

func writeTestBackupAt(t *testing.T, dir, org, version string, at time.Time) string {
	t.Helper()
	raw := fmt.Sprintf(`{"identifier":"Xelion-u1","instruction_id":"%[2]s","base_identifier":"Xelion","version":"%[1]s","archived":true,
		"label":"Custom Software - Xelion","provider":"custom","organization_id":"o1","category":"General","priority":5,
		"installation_script":"install %[1]s","condition":"","remove_script":"rm","run_on_success":true,
		"predefined_conditions":{"app_exists":{"enabled":true}},"profiles":[],
		"software_icon":{"id":"icon1","provider":"cloud-storage","filename":"x.png","md5_hash":"micon"},
		"downloads":[{"id":"dl-%[1]s","filename":"Xelion-%[1]s.dmg","md5_hash":"m-%[1]s","size":1}]}`, version, backupID(version))
	path := filepath.Join(dir, "xelion", fmt.Sprintf("%s-%s-%s.json", version, org, at.UTC().Format("20060102T150405Z")))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(backup{Format: 1, BackedUpAt: at.UTC().Format(time.RFC3339), OrganizationID: org, SmartSoftware: json.RawMessage(raw)})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setupRestore(t *testing.T) (*restoreFake, *App, *bytes.Buffer, string) {
	t.Helper()
	fake := &restoreFake{
		versions: []string{
			`{"identifier":"Xelion-u1","instruction_id":"cur","base_identifier":"Xelion","version":"9.8.0.1"}`,
			`{"identifier":"Xelion-u1","instruction_id":"older","base_identifier":"Xelion","version":"9.7.5.0","archived":true}`,
		},
		files: []addigy.File{
			{ID: "dl-9.7.0.0", Filename: "Xelion-9.7.0.0.dmg", MD5Hash: "m-9.7.0.0"},
			{ID: "dl-9.6.0.0", Filename: "Xelion-9.6.0.0.dmg", MD5Hash: "m-9.6.0.0"},
			{ID: "icon1", Filename: "x.png", MD5Hash: "micon"},
		},
	}
	srv := httptest.NewServer(fake.serve(t))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2"},
		In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}}
	return fake, app, &out, t.TempDir()
}

func TestRestoreAsNewVersion(t *testing.T) {
	fake, app, out, dir := setupRestore(t)
	path := writeTestBackup(t, dir, "o1", "9.7.0.0")
	if err := (&SmartSoftwareRestoreCmd{Refs: []string{path}, Yes: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if len(fake.created) != 0 || len(fake.newVersions) != 1 {
		t.Fatalf("created %d, new versions %d", len(fake.created), len(fake.newVersions))
	}
	if fake.nvPaths[0] != "/api/v2/o/o1/smart-software/cur/new-version" {
		t.Errorf("new-version on %s; want the item's highest version", fake.nvPaths[0])
	}
	body := fake.newVersions[0]
	if body["archived"] != true || body["version"] != "9.7.0.0" || body["installation_script"] != "install 9.7.0.0" || body["identifier"] != "Xelion-u1" {
		t.Errorf("body = %v", body)
	}
	if dl, _ := json.Marshal(body["downloads"]); string(dl) != `[{"id":"dl-9.7.0.0"}]` {
		t.Errorf("downloads = %s", dl)
	}
	if ic, _ := json.Marshal(body["software_icon"]); string(ic) != `{"id":"icon1","provider":"cloud-storage"}` {
		t.Errorf("icon = %s", ic)
	}
	for _, k := range []string{"label", "provider", "organization_id", "instruction_id"} {
		if _, ok := body[k]; ok {
			t.Errorf("server-managed field %q sent", k)
		}
	}
	if !strings.Contains(out.String(), "Restored Xelion 9.7.0.0 (instruction_id r1)") {
		t.Errorf("output:\n%s", out)
	}
	if w := app.Err.(*bytes.Buffer).String(); strings.Contains(w, "warning") {
		t.Errorf("unexpected warning: %s", w)
	}
}

func TestRestoreCreatesItem(t *testing.T) {
	fake, app, _, dir := setupRestore(t)
	fake.versions = nil // every version of the item is gone
	// Two versions of the same item: the first creates it, the second joins it.
	p1 := writeTestBackup(t, dir, "o1", "9.6.0.0")
	p2 := writeTestBackup(t, dir, "o1", "9.7.0.0")
	if err := (&SmartSoftwareRestoreCmd{Refs: []string{p1, p2}, Yes: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if len(fake.created) != 1 || len(fake.newVersions) != 1 {
		t.Fatalf("created %d, new versions %d", len(fake.created), len(fake.newVersions))
	}
	if fake.created[0]["version"] != "9.6.0.0" || fake.nvPaths[0] != "/api/v2/o/o1/smart-software/r1/new-version" {
		t.Errorf("created %v, then new-version on %s", fake.created[0]["version"], fake.nvPaths[0])
	}
}

func TestRestoreUsesReuploadedFile(t *testing.T) {
	fake, app, out, dir := setupRestore(t)
	fake.files[0].ID = "dl-again" // deleted, then uploaded again: same MD5, new ID
	path := writeTestBackup(t, dir, "o1", "9.7.0.0")
	if err := (&SmartSoftwareRestoreCmd{Refs: []string{path}, Yes: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if dl, _ := json.Marshal(fake.newVersions[0]["downloads"]); string(dl) != `[{"id":"dl-again"}]` {
		t.Errorf("downloads = %s", dl)
	}
	if !strings.Contains(out.String(), "uploaded again; was dl-9.7.0.0") {
		t.Errorf("output:\n%s", out)
	}
}

func TestRestoreRefuses(t *testing.T) {
	fake, app, _, dir := setupRestore(t)
	ok := writeTestBackup(t, dir, "o1", "9.7.0.0")
	missing := writeTestBackup(t, dir, "o1", "9.5.0.0") // its download was never uploaded
	exists := writeTestBackup(t, dir, "o1", "9.8.0.1")
	otherOrg := writeTestBackup(t, dir, "o2", "9.6.0.0")
	junk := filepath.Join(dir, "junk.json")
	os.WriteFile(junk, []byte(`{"hello":1}`), 0o600)

	err := (&SmartSoftwareRestoreCmd{Refs: []string{ok, missing, exists, otherOrg, junk, ok}, Yes: true}).Run(app)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"nothing was restored",
		"Xelion-9.5.0.0.dmg (md5 m-9.5.0.0)",
		"Xelion 9.8.0.1 is already in Addigy (instruction_id cur)",
		"backed up from organization o2",
		"not a backup",
		"the same version as " + ok,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if len(fake.created)+len(fake.newVersions) != 0 {
		t.Errorf("restored although a backup failed its checks")
	}
}

func TestRestoreDryRun(t *testing.T) {
	fake, app, out, dir := setupRestore(t)
	path := writeTestBackup(t, dir, "o1", "9.7.0.0")
	if err := (&SmartSoftwareRestoreCmd{Refs: []string{path}, DryRun: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if len(fake.created)+len(fake.newVersions) != 0 {
		t.Error("dry run restored")
	}
	if got := out.String(); !strings.Contains(got, "a new version of Xelion-u1, next to 9.8.0.1") || !strings.Contains(got, "come back archived") || !strings.Contains(got, "Dry run") {
		t.Errorf("output:\n%s", got)
	}
}

func TestRestoreWarnsWhenNotArchived(t *testing.T) {
	fake, app, _, dir := setupRestore(t)
	fake.unarchive = true
	path := writeTestBackup(t, dir, "o1", "9.7.0.0")
	if err := (&SmartSoftwareRestoreCmd{Refs: []string{path}, Yes: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	if w := app.Err.(*bytes.Buffer).String(); !strings.Contains(w, "did not keep Xelion 9.7.0.0 archived") {
		t.Errorf("stderr: %s", w)
	}
}

func TestRestoreByID(t *testing.T) {
	fake, app, _, dir := setupRestore(t)
	old := writeTestBackupAt(t, dir, "o1", "9.7.0.0", time.Now().Add(-time.Hour))
	newest := writeTestBackup(t, dir, "o1", "9.7.0.0")
	os.WriteFile(old, bytes.Replace(mustRead(t, old), []byte("install 9.7.0.0"), []byte("old script"), 1), 0o600)
	cmd := &SmartSoftwareRestoreCmd{Refs: []string{backupID("9.7.0.0")}, Yes: true, BackupDir: dir}
	if err := cmd.Run(app); err != nil {
		t.Fatal(err)
	}
	if len(fake.newVersions) != 1 || fake.newVersions[0]["installation_script"] != "install 9.7.0.0" {
		t.Errorf("restored %v; want the newest backup (%s)", fake.newVersions, newest)
	}
	if w := app.Err.(*bytes.Buffer).String(); !strings.Contains(w, "2 backups of "+backupID("9.7.0.0")+"; using the newest") {
		t.Errorf("stderr: %s", w)
	}

	err := (&SmartSoftwareRestoreCmd{Refs: []string{backupID("1.0.0")}, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "no backup of "+backupID("1.0.0")) || !strings.Contains(err.Error(), "smart-software backups") {
		t.Errorf("unknown ID: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBackupsList(t *testing.T) {
	_, app, out, dir := setupRestore(t)
	writeTestBackup(t, dir, "o1", "9.7.0.0") // ready
	writeTestBackup(t, dir, "o1", "9.5.0.0") // its download is gone
	writeTestBackup(t, dir, "o1", "9.8.0.1") // in Addigy again
	writeTestBackup(t, dir, "o2", "9.6.0.0") // another tenant
	// A deletion log of files delete is not a backup.
	os.MkdirAll(filepath.Join(dir, "files"), 0o700)
	os.WriteFile(filepath.Join(dir, "files", "deleted-x.json"), []byte(`{"addigyctl_file_deletion":1,"files":[]}`), 0o600)

	app.G.Output = "json"
	if err := (&SmartSoftwareBackupsCmd{BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	var got []backupEntry
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	status := map[string]string{}
	for _, e := range got {
		status[e.Version] = e.Status
	}
	want := map[string]string{"9.5.0.0": "needs-upload", "9.6.0.0": "other-organization", "9.7.0.0": "ready", "9.8.0.1": "in-addigy"}
	if fmt.Sprint(status) != fmt.Sprint(want) {
		t.Errorf("statuses = %v, want %v", status, want)
	}
	if got[0].Version != "9.5.0.0" || got[0].InstructionID != backupID("9.5.0.0") || len(got[0].NeedsUpload) != 1 {
		t.Errorf("first entry = %+v (want oldest version first)", got[0])
	}

	app.G.Output = ""
	out.Reset()
	if err := (&SmartSoftwareBackupsCmd{BackupDir: dir, Name: "XEL"}).Run(app); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"ready to restore", "needs upload: Xelion-9.5.0.0.dmg", "already in Addigy", "other organization", "4 backups in", "1 ready to restore"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("table lacks %q:\n%s", w, out)
		}
	}
}
