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
	"testing"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/config"
)

const (
	idDelete  = "11111111-2222-3333-4444-555555555555" // QGIS 3.40.1, active
	idQGIS338 = "11111111-2222-3333-4444-000000000338" // QGIS 3.38.0, archived
	idQGIS336 = "11111111-2222-3333-4444-000000000336" // QGIS 3.36.0, archived
	idZoom    = "11111111-2222-3333-4444-0000000000a0" // Zoom 6.0.0, archived
	idUnknown = "99999999-2222-3333-4444-555555555555"
)

func swFixture(id, name, version string, archived bool) string {
	return fmt.Sprintf(`{"identifier":"%[2]s-u1","instruction_id":"%[1]s","base_identifier":"%[2]s","version":"%[3]s","archived":%[4]t,"category":"General","downloads":[{"id":"f-%[3]s","filename":"%[2]s-%[3]s.dmg","md5_hash":"m-%[3]s","size":42}]}`,
		id, name, version, archived)
}

// deleteFake serves Smart Software versions (GET by id, and a query that
// returns them all, leaving the filtering to the client) and records DELETEs.
type deleteFake struct {
	versions map[string]string // instruction id -> JSON
	fail     map[string]int    // instruction id -> status its DELETE fails with
	deleted  []string          // instruction ids, in order
	attempts int               // DELETE requests
}

// setupDelete returns an app pointed at a deleteFake, with its backups going
// to a temporary folder.
func setupDelete(t *testing.T, in string) (*App, *bytes.Buffer, *deleteFake, string) {
	t.Helper()
	fake := &deleteFake{versions: map[string]string{
		idDelete:  swFixture(idDelete, "QGIS", "3.40.1", false),
		idQGIS338: swFixture(idQGIS338, "QGIS", "3.38.0", true),
		idQGIS336: swFixture(idQGIS336, "QGIS", "3.36.0", true),
		idZoom:    swFixture(idZoom, "Zoom", "6.0.0", true),
	}, fail: map[string]int{}}
	const swPath = "/api/v2/o/o1/smart-software/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, swPath)
		switch {
		case r.URL.Path == "/api/v2/oa/policies/query":
			io.WriteString(w, `[{"policyId":"p1","orgid":"o1"}]`)
		case r.URL.Path == "/api/v2/oa/smart-software/query":
			var all []string
			for _, v := range fake.versions {
				all = append(all, v)
			}
			fmt.Fprintf(w, `{"items":[%s],"metadata":{"page":1,"page_count":1,"per_page":100,"total":%d}}`, strings.Join(all, ","), len(all))
		case strings.HasPrefix(r.URL.Path, swPath) && r.Method == http.MethodGet:
			v, ok := fake.versions[id]
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"message":"custom software not found"}`)
				return
			}
			io.WriteString(w, v)
		case strings.HasPrefix(r.URL.Path, swPath) && r.Method == http.MethodDelete:
			fake.attempts++
			if status := fake.fail[id]; status != 0 {
				w.WriteHeader(status)
				io.WriteString(w, `{"message":"Do not have permission to perform this action."}`)
				return
			}
			fake.deleted = append(fake.deleted, id)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2"},
		In: strings.NewReader(in), Out: &out, Err: &bytes.Buffer{}}
	return app, &out, fake, t.TempDir()
}

func backupFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestDeleteBacksUpFirst(t *testing.T) {
	app, out, fake, dir := setupDelete(t, "")
	if err := (&SmartSoftwareDeleteCmd{IDs: []string{idDelete}, Yes: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.deleted, []string{idDelete}) {
		t.Fatalf("deleted %v", fake.deleted)
	}
	files := backupFiles(t, dir)
	if len(files) != 1 || !strings.HasPrefix(files[0], filepath.Join(dir, "qgis", "3.40.1-"+idDelete+"-")) {
		t.Fatalf("backups = %v", files)
	}
	for _, p := range []string{files[0], filepath.Dir(files[0])} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s should be private: %v, %v", p, st.Mode(), err)
		}
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Format int                  `json:"addigyctl_backup"`
		Org    string               `json:"organization_id"`
		SW     addigy.SmartSoftware `json:"smart_software"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Format != 1 || got.Org != "o1" || got.SW.InstructionID != idDelete || len(got.SW.Downloads) != 1 || got.SW.Downloads[0].MD5Hash != "m-3.40.1" {
		t.Errorf("backup = %s", b)
	}
	if !strings.Contains(out.String(), "Deleted QGIS 3.40.1") || !strings.Contains(out.String(), "Backup: "+files[0]) {
		t.Errorf("output:\n%s", out)
	}
	if strings.Contains(out.String(), " of ") {
		t.Errorf("one version needs no \"Deleted n of m\" summary:\n%s", out)
	}
}

func TestDeleteDryRun(t *testing.T) {
	app, out, fake, dir := setupDelete(t, "")
	if err := (&SmartSoftwareDeleteCmd{IDs: []string{idDelete}, DryRun: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if fake.attempts != 0 || len(backupFiles(t, dir)) != 0 {
		t.Errorf("dry run deleted %d, backed up %v", fake.attempts, backupFiles(t, dir))
	}
	if !strings.Contains(out.String(), "QGIS-3.40.1.dmg") || !strings.Contains(out.String(), "Dry run") || !strings.Contains(out.String(), "assigned to policies") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDeleteCancelled(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "n\n")
	err := (&SmartSoftwareDeleteCmd{IDs: []string{idDelete}, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("err = %v", err)
	}
	if fake.attempts != 0 || len(backupFiles(t, dir)) != 0 {
		t.Errorf("cancel deleted %d, backed up %v", fake.attempts, backupFiles(t, dir))
	}
}

func TestDeleteStopsWhenBackupFails(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	// A file where the backup folder should be.
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := (&SmartSoftwareDeleteCmd{IDs: []string{idQGIS336, idQGIS338}, Yes: true, BackupDir: blocked}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "2 versions could not be deleted") {
		t.Errorf("err = %v", err)
	}
	if fake.attempts != 0 {
		t.Errorf("deleted %d times without a backup", fake.attempts)
	}
	if got := app.Err.(*bytes.Buffer).String(); !strings.Contains(got, "could not write the backup") || !strings.Contains(got, "1 version not attempted") {
		t.Errorf("stderr:\n%s", got)
	}
}

func TestDeleteNoBackup(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	if err := (&SmartSoftwareDeleteCmd{IDs: []string{idDelete}, Yes: true, NoBackup: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 1 || len(backupFiles(t, dir)) != 0 {
		t.Errorf("deleted %v, backed up %v", fake.deleted, backupFiles(t, dir))
	}
}

func TestDeleteNeedsInstructionIDs(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	err := (&SmartSoftwareDeleteCmd{IDs: []string{idDelete, "QGIS", "QGIS-u1"}, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), `"QGIS", "QGIS-u1": not a version ID`) {
		t.Errorf("err = %v", err)
	}
	if fake.attempts != 0 {
		t.Errorf("deleted %d", fake.attempts)
	}
}

func TestDeleteArgs(t *testing.T) {
	for _, c := range []struct {
		cmd  SmartSoftwareDeleteCmd
		want string
	}{
		{SmartSoftwareDeleteCmd{}, "give the version IDs"},
		{SmartSoftwareDeleteCmd{Archived: true}, "--archived needs --name"},
		{SmartSoftwareDeleteCmd{IDs: []string{idDelete}, Archived: true, Name: "q"}, "not both"},
		{SmartSoftwareDeleteCmd{IDs: []string{idDelete}, Name: "q"}, "not both"},
	} {
		if err := c.cmd.Run(&App{G: &Globals{}}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want %q", c.cmd, err, c.want)
		}
	}
}

func TestDeleteMultiple(t *testing.T) {
	app, out, fake, dir := setupDelete(t, "")
	// Out of order and with a duplicate: deleted once each, oldest first.
	if err := (&SmartSoftwareDeleteCmd{IDs: []string{idQGIS338, idQGIS336, idQGIS338}, Yes: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.deleted, []string{idQGIS336, idQGIS338}) {
		t.Errorf("deleted %v", fake.deleted)
	}
	if files := backupFiles(t, dir); len(files) != 2 {
		t.Errorf("backups = %v", files)
	}
	got := out.String()
	if strings.Count(got, "warning:") != 1 || !strings.Contains(got, "Deleted QGIS 3.36.0") || !strings.Contains(got, "Deleted 2 of 2 versions.") {
		t.Errorf("output:\n%s", got)
	}
}

func TestDeleteMultipleAsks(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "y\n")
	if err := (&SmartSoftwareDeleteCmd{IDs: []string{idQGIS336, idQGIS338}, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(app.Err.(*bytes.Buffer).String(), "Delete 2 versions? [y/N]") || len(fake.deleted) != 2 {
		t.Errorf("stderr:\n%s\ndeleted %v", app.Err, fake.deleted)
	}
}

func TestDeleteUnknownID(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	err := (&SmartSoftwareDeleteCmd{IDs: []string{idQGIS336, idUnknown}, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted: no smart software with instruction id "+idUnknown) {
		t.Errorf("err = %v", err)
	}
	if fake.attempts != 0 || len(backupFiles(t, dir)) != 0 {
		t.Errorf("deleted %d, backed up %v", fake.attempts, backupFiles(t, dir))
	}
}

func TestDeleteArchived(t *testing.T) {
	app, out, fake, dir := setupDelete(t, "")
	if err := (&SmartSoftwareDeleteCmd{Archived: true, Name: "qgis", Yes: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	// Not the active 3.40.1, and not Zoom.
	if !slices.Equal(fake.deleted, []string{idQGIS336, idQGIS338}) {
		t.Errorf("deleted %v", fake.deleted)
	}
	if !strings.Contains(out.String(), "Deleted 2 of 2 versions.") {
		t.Errorf("output:\n%s", out)
	}

	out.Reset()
	if err := (&SmartSoftwareDeleteCmd{Archived: true, Name: "teams", Yes: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to delete") {
		t.Errorf("no match: %s", out)
	}
}

func TestDeletePartialFailure(t *testing.T) {
	app, out, fake, dir := setupDelete(t, "")
	app.G.Output = "json"
	fake.fail[idQGIS336] = http.StatusInternalServerError
	err := (&SmartSoftwareDeleteCmd{IDs: []string{idQGIS336, idQGIS338, idZoom}, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "1 version could not be deleted") {
		t.Errorf("err = %v", err)
	}
	if !slices.Equal(fake.deleted, []string{idQGIS338, idZoom}) {
		t.Errorf("deleted %v; should carry on past a failed delete", fake.deleted)
	}
	if files := backupFiles(t, dir); len(files) != 2 {
		t.Errorf("backups = %v; the failed version's backup should be gone", files)
	}
	var res deleteResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if res.DryRun || len(res.Versions) != 3 {
		t.Fatalf("result = %+v", res)
	}
	v := res.Versions[0]
	if v.InstructionID != idQGIS336 || v.Deleted || v.Backup != "" || !strings.Contains(v.Error, "backup was removed") {
		t.Errorf("failed version = %+v", v)
	}
	if v := res.Versions[1]; !v.Deleted || v.Backup == "" || v.Error != "" || len(v.Downloads) != 1 {
		t.Errorf("deleted version = %+v", v)
	}
}

func TestDeleteStopsWhenForbidden(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	fake.fail[idQGIS336] = http.StatusForbidden
	err := (&SmartSoftwareDeleteCmd{IDs: []string{idQGIS336, idQGIS338, idZoom}, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "3 versions could not be deleted") {
		t.Errorf("err = %v", err)
	}
	if fake.attempts != 1 {
		t.Errorf("%d DELETEs; a 403 should stop the run after the first", fake.attempts)
	}
	if files := backupFiles(t, dir); len(files) != 0 {
		t.Errorf("backups left behind: %v", files)
	}
}

func TestDeleteFailureRemovesBackup(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	fake.fail[idDelete] = http.StatusForbidden
	err := (&SmartSoftwareDeleteCmd{IDs: []string{idDelete}, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "backup was removed") {
		t.Errorf("err = %v", err)
	}
	if files := backupFiles(t, dir); len(files) != 0 {
		t.Errorf("backup of an undeleted version left behind: %v", files)
	}
}

func TestDeleteBackupDir(t *testing.T) {
	app := &App{G: &Globals{}, Cfg: config.File{BackupDir: "/cfg/backups"}}
	if d, _ := backupDir(app, "/flag"); d != "/flag" {
		t.Errorf("--backup-dir: %q", d)
	}
	if d, _ := backupDir(app, ""); d != "/cfg/backups" {
		t.Errorf("config backup_dir: %q", d)
	}
	app.Cfg.BackupDir = ""
	if d, err := backupDir(app, ""); err != nil || !strings.HasSuffix(d, filepath.Join("addigyctl", "backups")) {
		t.Errorf("default: %q, %v", d, err)
	}
}

func TestBackupPath(t *testing.T) {
	now := time.Date(2026, 10, 7, 14, 30, 0, 0, time.UTC)
	s := &addigy.SmartSoftware{BaseIdentifier: "Microsoft Teams", Version: "25.1 (beta)", InstructionID: "i1"}
	if p := backupPath("/b", s, now); p != "/b/microsoft-teams/25.1-beta-i1-20261007T143000Z.json" {
		t.Errorf("backupPath = %q", p)
	}
}
