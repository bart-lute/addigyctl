package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/config"
)

const (
	idDelete       = "11111111-2222-3333-4444-555555555555"
	fixtureDelete  = `{"identifier":"QGIS-u1","instruction_id":"` + idDelete + `","base_identifier":"QGIS","version":"3.40.1","category":"General","downloads":[{"id":"f1","filename":"QGIS-3.40.1.dmg","md5_hash":"m1","size":42}]}`
	deleteEndpoint = "/api/v2/o/o1/smart-software/" + idDelete
)

// deleteFake counts DELETEs; a non-zero status makes them fail with it.
type deleteFake struct {
	deletes int
	status  int
}

// setupDelete returns an app pointed at a fake that serves fixtureDelete,
// with its backups going to a temporary folder.
func setupDelete(t *testing.T, in string) (*App, *bytes.Buffer, *deleteFake, string) {
	t.Helper()
	fake := &deleteFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oa/policies/query":
			io.WriteString(w, `[{"policyId":"p1","orgid":"o1"}]`)
		case r.URL.Path == deleteEndpoint && r.Method == http.MethodGet:
			io.WriteString(w, fixtureDelete)
		case r.URL.Path == deleteEndpoint && r.Method == http.MethodDelete:
			fake.deletes++
			if fake.status != 0 {
				w.WriteHeader(fake.status)
				io.WriteString(w, `{"message":"Do not have permission to perform this action."}`)
			}
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
	if err := (&SmartSoftwareDeleteCmd{ID: idDelete, Yes: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if fake.deletes != 1 {
		t.Fatalf("expected one DELETE, got %d", fake.deletes)
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
	if got.Format != 1 || got.Org != "o1" || got.SW.InstructionID != idDelete || len(got.SW.Downloads) != 1 || got.SW.Downloads[0].MD5Hash != "m1" {
		t.Errorf("backup = %s", b)
	}
	if !strings.Contains(out.String(), "Deleted QGIS 3.40.1") || !strings.Contains(out.String(), "Backup: "+files[0]) {
		t.Errorf("output:\n%s", out)
	}
}

func TestDeleteDryRun(t *testing.T) {
	app, out, fake, dir := setupDelete(t, "")
	if err := (&SmartSoftwareDeleteCmd{ID: idDelete, DryRun: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if fake.deletes != 0 || len(backupFiles(t, dir)) != 0 {
		t.Errorf("dry run deleted %d, backed up %v", fake.deletes, backupFiles(t, dir))
	}
	if !strings.Contains(out.String(), "QGIS-3.40.1.dmg") || !strings.Contains(out.String(), "Dry run") || !strings.Contains(out.String(), "assigned to policies") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDeleteCancelled(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "n\n")
	err := (&SmartSoftwareDeleteCmd{ID: idDelete, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("err = %v", err)
	}
	if fake.deletes != 0 || len(backupFiles(t, dir)) != 0 {
		t.Errorf("cancel deleted %d, backed up %v", fake.deletes, backupFiles(t, dir))
	}
}

func TestDeleteStopsWhenBackupFails(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	// A file where the backup folder should be.
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := (&SmartSoftwareDeleteCmd{ID: idDelete, Yes: true, BackupDir: blocked}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("err = %v", err)
	}
	if fake.deletes != 0 {
		t.Errorf("deleted %d times without a backup", fake.deletes)
	}
}

func TestDeleteNoBackup(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	if err := (&SmartSoftwareDeleteCmd{ID: idDelete, Yes: true, NoBackup: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if fake.deletes != 1 || len(backupFiles(t, dir)) != 0 {
		t.Errorf("deleted %d, backed up %v", fake.deletes, backupFiles(t, dir))
	}
}

func TestDeleteNeedsInstructionID(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	for _, ref := range []string{"QGIS", "QGIS-u1"} {
		err := (&SmartSoftwareDeleteCmd{ID: ref, Yes: true, BackupDir: dir}).Run(app)
		if err == nil || !strings.Contains(err.Error(), "not a version ID") {
			t.Errorf("%q: err = %v", ref, err)
		}
	}
	if fake.deletes != 0 {
		t.Errorf("deleted %d", fake.deletes)
	}
}

func TestDeleteBackupDir(t *testing.T) {
	app := &App{G: &Globals{}, Cfg: config.File{BackupDir: "/cfg/backups"}}
	if d, _ := (&SmartSoftwareDeleteCmd{BackupDir: "/flag"}).backupDir(app); d != "/flag" {
		t.Errorf("--backup-dir: %q", d)
	}
	if d, _ := (&SmartSoftwareDeleteCmd{}).backupDir(app); d != "/cfg/backups" {
		t.Errorf("config backup_dir: %q", d)
	}
	app.Cfg.BackupDir = ""
	if d, err := (&SmartSoftwareDeleteCmd{}).backupDir(app); err != nil || !strings.HasSuffix(d, filepath.Join("addigyctl", "backups")) {
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

func TestDeleteFailureRemovesBackup(t *testing.T) {
	app, _, fake, dir := setupDelete(t, "")
	fake.status = http.StatusForbidden
	err := (&SmartSoftwareDeleteCmd{ID: idDelete, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "backup was removed") {
		t.Errorf("err = %v", err)
	}
	if files := backupFiles(t, dir); len(files) != 0 {
		t.Errorf("backup of an undeleted version left behind: %v", files)
	}
}
