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

	"github.com/ginkio/addigyctl/internal/addigy"
)

// fakeFiles serves uploaded files, their usages and deletes.
type fakeFiles struct {
	files      []addigy.File
	used       map[string]string // file ID -> name of what uses it
	usedLater  map[string]string // added to used from the second usage call on
	failDelete string            // this file's DELETE fails
	denyDelete bool              // every DELETE is forbidden
	usageCalls int
	deleted    []string
	attempts   int // DELETE requests
}

func (f *fakeFiles) serve(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oa/policies/query":
			io.WriteString(w, `[{"policyId":"p1","orgid":"o1"}]`)
		case r.URL.Path == "/api/v2/oa/files/query":
			b, _ := json.Marshal(f.files)
			fmt.Fprintf(w, `{"items":%s,"metadata":{"page":1,"page_count":1,"per_page":100,"total":%d}}`, b, len(f.files))
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
		case r.URL.Path == "/api/v2/files/usage":
			f.usageCalls++
			var q struct {
				FileIDs []string `json:"file_ids"`
			}
			json.NewDecoder(r.Body).Decode(&q)
			usages := []addigy.FileUsage{}
			for _, id := range q.FileIDs {
				name, ok := f.used[id]
				if !ok && f.usageCalls > 1 {
					name, ok = f.usedLater[id]
				}
				if ok {
					usages = append(usages, addigy.FileUsage{FileID: id, FeatureType: "ansible-custom-software", ItemID: "i1", ItemName: name})
				}
			}
			json.NewEncoder(w).Encode(usages)
		case strings.HasPrefix(r.URL.Path, "/api/v2/o/o1/files/") && r.Method == http.MethodDelete:
			id := strings.TrimPrefix(r.URL.Path, "/api/v2/o/o1/files/")
			f.attempts++
			if f.denyDelete {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"message":"Do not have permission to perform this action."}`)
				return
			}
			if id == f.failDelete {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"message":"file is in use"}`)
				return
			}
			f.deleted = append(f.deleted, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}
}

// setupFilesDelete: old.pkg (2021, unused), citrix-1.pkg and citrix-2.pkg
// (2024/2025, unused), app.pkg (in use).
func setupFilesDelete(t *testing.T) (*fakeFiles, *App, *bytes.Buffer, string) {
	t.Helper()
	fake := &fakeFiles{
		files: []addigy.File{
			{ID: "old", Filename: "old.pkg", Size: 1000, Created: "2021-06-01T10:00:00Z"},
			{ID: "cx1", Filename: "citrix-1.pkg", Size: 2000, Created: "2024-06-01T10:00:00Z"},
			{ID: "cx2", Filename: "Citrix-2.pkg", Size: 3000, Created: "2025-06-01T10:00:00Z"},
			{ID: "app", Filename: "app.pkg", Size: 4000, Created: "2020-01-01T10:00:00Z"},
		},
		used: map[string]string{"app": "App (1.0)"},
	}
	srv := httptest.NewServer(fake.serve(t))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2"},
		In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}}
	return fake, app, &out, t.TempDir()
}

func readDeletionLog(t *testing.T, dir string) (deletionLog, string) {
	t.Helper()
	logs, _ := filepath.Glob(filepath.Join(dir, "files", "deleted-*.json"))
	if len(logs) != 1 {
		t.Fatalf("deletion logs: %v", logs)
	}
	b, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	var l deletionLog
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	return l, logs[0]
}

func TestFilesDeleteByID(t *testing.T) {
	fake, app, out, dir := setupFilesDelete(t)
	if err := (&FilesDeleteCmd{IDs: []string{"old", "cx1"}, Yes: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.deleted, []string{"old", "cx1"}) {
		t.Errorf("deleted %v", fake.deleted)
	}
	l, path := readDeletionLog(t, dir)
	if l.Pending || l.OrganizationID != "o1" || len(l.Files) != 2 || !l.Files[0].Deleted || l.Files[1].Filename != "citrix-1.pkg" {
		t.Errorf("log = %+v", l)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v, want 0600", st.Mode())
	}
	if !strings.Contains(out.String(), "Deleted 2 files of 2 (3.0 KB)") {
		t.Errorf("output:\n%s", out)
	}
}

func TestFilesDeleteRefusesInUse(t *testing.T) {
	fake, app, _, dir := setupFilesDelete(t)
	err := (&FilesDeleteCmd{IDs: []string{"old", "app"}, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "in use") || !strings.Contains(err.Error(), "App (1.0)") {
		t.Errorf("err = %v", err)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("deleted %v; an in-use file should stop the whole run", fake.deleted)
	}
}

func TestFilesDeleteUnknownID(t *testing.T) {
	fake, app, _, dir := setupFilesDelete(t)
	err := (&FilesDeleteCmd{IDs: []string{"old", "nope"}, Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "no file with ID nope") {
		t.Errorf("err = %v", err)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("deleted %v", fake.deleted)
	}
}

func TestFilesDeleteUnused(t *testing.T) {
	for _, c := range []struct {
		cmd  FilesDeleteCmd
		want []string
	}{
		{FilesDeleteCmd{Name: "CITRIX"}, []string{"cx1", "cx2"}},
		{FilesDeleteCmd{Before: "2024-12-31"}, []string{"old", "cx1"}}, // not app.pkg: in use
		{FilesDeleteCmd{Name: "citrix", Before: "2025-01-01"}, []string{"cx1"}},
	} {
		fake, app, _, dir := setupFilesDelete(t)
		cmd := c.cmd
		cmd.Unused, cmd.Yes, cmd.BackupDir = true, true, dir
		if err := cmd.Run(app); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(fake.deleted, c.want) {
			t.Errorf("--name %q --before %q deleted %v, want %v", c.cmd.Name, c.cmd.Before, fake.deleted, c.want)
		}
	}
}

func TestFilesDeleteDryRun(t *testing.T) {
	fake, app, out, dir := setupFilesDelete(t)
	if err := (&FilesDeleteCmd{Unused: true, Name: "citrix", DryRun: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("dry run deleted %v", fake.deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "files")); !os.IsNotExist(err) {
		t.Errorf("dry run wrote a log: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Citrix-2.pkg") || !strings.Contains(got, "2 files, 5.0 KB") || !strings.Contains(got, "Dry run") {
		t.Errorf("output:\n%s", got)
	}
	// Oldest first, whatever order Addigy returns them in.
	fake.files[1], fake.files[2] = fake.files[2], fake.files[1]
	out.Reset()
	if err := (&FilesDeleteCmd{Unused: true, Name: "citrix", DryRun: true, BackupDir: dir}).Run(app); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Index(got, "citrix-1.pkg") > strings.Index(got, "Citrix-2.pkg") {
		t.Errorf("preview not oldest first:\n%s", got)
	}
}

func TestFilesDeleteCancelled(t *testing.T) {
	fake, app, _, dir := setupFilesDelete(t)
	app.In = strings.NewReader("n\n")
	err := (&FilesDeleteCmd{IDs: []string{"old"}, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "cancelled") || len(fake.deleted) != 0 {
		t.Errorf("err = %v, deleted %v", err, fake.deleted)
	}
}

func TestFilesDeletePartialFailure(t *testing.T) {
	fake, app, out, dir := setupFilesDelete(t)
	fake.failDelete = "cx1"
	fake.usedLater = map[string]string{"cx2": "Citrix (25.0)"} // used while the question was open
	err := (&FilesDeleteCmd{Unused: true, Before: "2026-01-01", Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "2 files could not be deleted") {
		t.Errorf("err = %v", err)
	}
	if !slices.Equal(fake.deleted, []string{"old"}) {
		t.Errorf("deleted %v; should carry on past a failure but skip a file now in use", fake.deleted)
	}
	l, _ := readDeletionLog(t, dir)
	byID := map[string]fileDeleteEntry{}
	for _, f := range l.Files {
		byID[f.ID] = f
	}
	if !byID["old"].Deleted || byID["cx1"].Deleted || !strings.Contains(byID["cx1"].Error, "in use") ||
		byID["cx2"].Deleted || !strings.Contains(byID["cx2"].Error, "now in use by Citrix (25.0)") {
		t.Errorf("log = %+v", l.Files)
	}
	if !strings.Contains(out.String(), "Deleted 1 file of 3") {
		t.Errorf("output:\n%s", out)
	}
}

func TestFilesDeleteNothingDeletedRemovesLog(t *testing.T) {
	fake, app, _, dir := setupFilesDelete(t)
	fake.failDelete = "old"
	if err := (&FilesDeleteCmd{IDs: []string{"old"}, Yes: true, BackupDir: dir}).Run(app); err == nil {
		t.Error("expected an error")
	}
	if logs, _ := filepath.Glob(filepath.Join(dir, "files", "*.json")); len(logs) != 0 {
		t.Errorf("log left behind although nothing was deleted: %v", logs)
	}
}

func TestFilesDeleteArgs(t *testing.T) {
	for _, c := range []struct {
		cmd  FilesDeleteCmd
		want string
	}{
		{FilesDeleteCmd{}, "give the IDs"},
		{FilesDeleteCmd{Unused: true}, "needs --name and/or --before"},
		{FilesDeleteCmd{IDs: []string{"x"}, Unused: true, Name: "y"}, "not both"},
		{FilesDeleteCmd{IDs: []string{"x"}, Name: "y"}, "not both"},
	} {
		if err := c.cmd.Run(&App{G: &Globals{}}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want %q", c.cmd, err, c.want)
		}
	}
	_, app, _, _ := setupFilesDelete(t)
	if err := (&FilesDeleteCmd{Unused: true, Before: "31-12-2024"}).Run(app); err == nil || !strings.Contains(err.Error(), "want a date") {
		t.Errorf("bad --before: %v", err)
	}
}

func TestFilesDeleteStopsWhenForbidden(t *testing.T) {
	fake, app, _, dir := setupFilesDelete(t)
	fake.denyDelete = true
	err := (&FilesDeleteCmd{Unused: true, Before: "2026-01-01", Yes: true, BackupDir: dir}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "3 files could not be deleted") {
		t.Errorf("err = %v", err)
	}
	if fake.attempts != 1 {
		t.Errorf("%d DELETEs; a 403 should stop the run after the first", fake.attempts)
	}
	if got := app.Err.(*bytes.Buffer).String(); !strings.Contains(got, "stopped: the API key may not delete files; 2 files not attempted") {
		t.Errorf("stderr:\n%s", got)
	}
	if logs, _ := filepath.Glob(filepath.Join(dir, "files", "*.json")); len(logs) != 0 {
		t.Errorf("log left behind: %v", logs)
	}
}
