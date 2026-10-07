package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

// ---- delete -----------------------------------------------------------------

type FilesDeleteCmd struct {
	IDs       []string `arg:"" optional:"" name:"file-id" help:"IDs of the files to delete, as files list shows them (-o csv or -j for the long IDs of older files)."`
	Unused    bool     `help:"Delete the files nothing uses that match --name and/or --before (one of them is required)."`
	Name      string   `help:"With --unused: only files whose name contains this text (case-insensitive)."`
	Before    string   `placeholder:"DATE" help:"With --unused: only files uploaded before this date (YYYY-MM-DD, in the configured time zone)."`
	DryRun    bool     `name:"dry-run" help:"Show the files that would be deleted, without deleting them."`
	Yes       bool     `help:"Delete without asking for confirmation."`
	BackupDir string   `name:"backup-dir" placeholder:"DIR" help:"Folder for the deletion log (default: \"backup_dir\" in the config file, else <user config dir>/addigyctl/backups)."`
}

func (c *FilesDeleteCmd) Run(app *App) error {
	if err := app.noCSV("files delete"); err != nil {
		return err
	}
	switch {
	case len(c.IDs) > 0 && (c.Unused || c.Name != "" || c.Before != ""):
		return errors.New("give file IDs or --unused, not both")
	case len(c.IDs) == 0 && !c.Unused:
		return errors.New("give the IDs of the files to delete, or --unused with --name and/or --before")
	case c.Unused && c.Name == "" && c.Before == "":
		return errors.New("--unused needs --name and/or --before, so it never means every unused file at once")
	}
	style, err := app.DateStyle()
	if err != nil {
		return err
	}
	var before time.Time
	if c.Before != "" {
		if before, err = time.ParseInLocation("2006-01-02", c.Before, style.Loc); err != nil {
			return fmt.Errorf("--before %q: want a date like 2023-01-31", c.Before)
		}
	}

	var chosen []fileUses
	if c.Unused {
		all, err := allFileUses(app)
		if err != nil {
			return err
		}
		needle := strings.ToLower(c.Name)
		for _, f := range all {
			if len(f.Usages) == 0 && strings.Contains(strings.ToLower(f.Filename), needle) && (before.IsZero() || createdBefore(f.File, before)) {
				chosen = append(chosen, f)
			}
		}
	} else {
		if chosen, err = filesByID(app, c.IDs); err != nil {
			return err
		}
		var inUse []string
		for _, f := range chosen {
			if len(f.Usages) > 0 {
				inUse = append(inUse, fmt.Sprintf("  %s  (used by %s)", f.Filename, fileUsedBy(f.Usages)))
			}
		}
		if len(inUse) > 0 {
			return fmt.Errorf("nothing was deleted: these files are in use\n%s", strings.Join(inUse, "\n"))
		}
	}
	if len(chosen) == 0 {
		if app.json() {
			return output.JSON(app.Out, fileDeleteResult{DryRun: c.DryRun, Files: []fileDeleteEntry{}})
		}
		fmt.Fprintln(app.Out, "No unused files match; nothing to delete.")
		return nil
	}

	sortFileUses(chosen, "created", false) // oldest first, the easiest to review

	preview := app.Out
	if app.json() {
		preview = app.Err
	}
	if err := printFilesPreview(app, preview, style, chosen); err != nil {
		return err
	}
	result := fileDeleteResult{DryRun: c.DryRun, Files: make([]fileDeleteEntry, len(chosen))}
	for i, f := range chosen {
		result.Files[i] = fileDeleteEntry{File: f.File}
	}
	if c.DryRun {
		if app.json() {
			return output.JSON(app.Out, result)
		}
		fmt.Fprintln(app.Out, "\nDry run: nothing was deleted.")
		return nil
	}
	if !c.Yes {
		fmt.Fprintln(app.Err, "Deleted files are gone for good: Addigy keeps no copy, and addigyctl can't download them first.")
		ok, err := app.confirm(fmt.Sprintf("Delete %s?", plural(len(chosen), "file")))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled; nothing was deleted")
		}
	}

	api, err := app.API()
	if err != nil {
		return err
	}
	org, err := app.OrgID()
	if err != nil {
		return err
	}
	// Something may have started using a file while the question was open.
	fresh, err := withUsages(app, filesOf(chosen))
	if err != nil {
		return err
	}
	// Create the log before deleting, so a log that can't be written stops
	// the run instead of leaving deletions unrecorded.
	dir, err := backupDir(app, c.BackupDir)
	if err != nil {
		return err
	}
	now := time.Now()
	result.Log = filepath.Join(dir, "files", "deleted-"+now.UTC().Format("20060102T150405Z")+".json")
	if err := writeDeletionLog(result.Log, org, now, result.Files, true); err != nil {
		return fmt.Errorf("could not write the deletion log, so nothing was deleted: %w", err)
	}

	var deleted, failed int
	var freed int64
	for i, f := range fresh {
		e := &result.Files[i]
		var denied bool
		if len(f.Usages) > 0 {
			e.Error = "now in use by " + fileUsedBy(f.Usages)
		} else if err := api.DeleteFile(app.Ctx, org, f.ID); err != nil {
			e.Error = err.Error()
			var apiErr *addigy.APIError
			denied = errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden)
		} else {
			e.Deleted = true
		}
		if e.Deleted {
			deleted++
			freed += f.Size
			continue
		}
		failed++
		fmt.Fprintf(app.Err, "not deleted: %s (%s): %s\n", f.Filename, f.ID, e.Error)
		// Not being allowed to delete holds for every file: don't try the rest.
		if denied {
			rest := result.Files[i+1:]
			for j := range rest {
				rest[j].Error = "not attempted: the API key may not delete files"
			}
			failed += len(rest)
			if len(rest) > 0 {
				fmt.Fprintf(app.Err, "stopped: the API key may not delete files; %s not attempted\n", plural(len(rest), "file"))
			}
			break
		}
	}
	if deleted == 0 {
		os.Remove(result.Log) // it would only record that nothing happened
		result.Log = ""
	} else if err := writeDeletionLog(result.Log, org, now, result.Files, false); err != nil {
		fmt.Fprintf(app.Err, "warning: could not record the results in %s: %v\n", result.Log, err)
	}

	if app.json() {
		if err := output.JSON(app.Out, result); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(app.Out, "\nDeleted %s of %d (%s).\n", plural(deleted, "file"), len(chosen), humanSize(freed))
		if result.Log != "" {
			fmt.Fprintf(app.Out, "Log: %s\n", result.Log)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%s could not be deleted (see above)", plural(failed, "file"))
	}
	return nil
}

// filesByID fetches the given files with their usages. Unknown IDs are an
// error, naming every one of them.
func filesByID(app *App, ids []string) ([]fileUses, error) {
	api, err := app.API()
	if err != nil {
		return nil, err
	}
	var files []addigy.File
	var unknown []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		f, err := api.File(app.Ctx, id)
		var apiErr *addigy.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			unknown = append(unknown, id)
			continue
		}
		if err != nil {
			return nil, err
		}
		files = append(files, *f)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("nothing was deleted: no file with ID %s", strings.Join(unknown, ", "))
	}
	return withUsages(app, files)
}

func filesOf(fs []fileUses) []addigy.File {
	out := make([]addigy.File, len(fs))
	for i, f := range fs {
		out[i] = f.File
	}
	return out
}

// createdBefore reports whether f was uploaded before t. A file whose upload
// time can't be read never is, so it is never picked by date.
func createdBefore(f addigy.File, t time.Time) bool {
	c, err := time.Parse(time.RFC3339, f.Created)
	return err == nil && c.Before(t)
}

func printFilesPreview(app *App, w io.Writer, style output.DateStyle, files []fileUses) error {
	var total int64
	rows := make([][]string, len(files))
	for i, f := range files {
		total += f.Size
		rows[i] = []string{output.Truncate(f.ID, 36), app.cell(output.Truncate(f.Filename, 60)), humanSize(f.Size), app.cell(style.DateTime(f.Created))}
	}
	if err := output.Rows(w, output.FormatTable, []string{"ID", "FILENAME", "SIZE", "CREATED"}, rows, app.borders()); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%s, %s\n", plural(len(files), "file"), humanSize(total))
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

type fileDeleteResult struct {
	DryRun bool              `json:"dry_run"`
	Log    string            `json:"log,omitempty"` // the deletion log; empty on a dry run or when nothing was deleted
	Files  []fileDeleteEntry `json:"files"`
}

type fileDeleteEntry struct {
	addigy.File
	Deleted bool   `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

// deletionLog is a deletion log file: what was (to be) deleted, with each
// file's metadata. Pending is true until the deletes have run.
type deletionLog struct {
	Format         int               `json:"addigyctl_file_deletion"`
	DeletedAt      string            `json:"deleted_at"`
	OrganizationID string            `json:"organization_id"`
	Pending        bool              `json:"pending,omitempty"`
	Files          []fileDeleteEntry `json:"files"`
}

// writeDeletionLog creates the log (pending) or, afterwards, fills in the
// results.
func writeDeletionLog(path, orgID string, now time.Time, files []fileDeleteEntry, pending bool) error {
	b, err := json.Marshal(deletionLog{Format: 1, DeletedAt: now.UTC().Format(time.RFC3339), OrganizationID: orgID, Pending: pending, Files: files})
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	if pending {
		return writePrivate(path, buf.Bytes())
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}
