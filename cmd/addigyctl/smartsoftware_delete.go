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
	"slices"
	"strings"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/config"
	"github.com/ginkio/addigyctl/internal/output"
	"github.com/ginkio/addigyctl/internal/swfolder"
)

// ---- delete -----------------------------------------------------------------

type SmartSoftwareDeleteCmd struct {
	IDs       []string `arg:"" optional:"" name:"instruction-id" help:"Version IDs (instruction_id) of the versions to delete, as smart-software list shows them."`
	Archived  bool     `help:"Delete the archived versions of the items whose name contains --name (required)."`
	Name      string   `help:"With --archived: only items whose name contains this text (case-insensitive)."`
	DryRun    bool     `name:"dry-run" help:"Show what would be deleted and where the backups would go, without doing either."`
	Yes       bool     `help:"Delete without asking for confirmation."`
	NoBackup  bool     `name:"no-backup" help:"Delete without writing backups first."`
	BackupDir string   `name:"backup-dir" placeholder:"DIR" help:"Folder for backups (default: \"backup_dir\" in the config file, else <user config dir>/addigyctl/backups)."`
}

func (c *SmartSoftwareDeleteCmd) Run(app *App) error {
	if err := app.noCSV("smart-software delete"); err != nil {
		return err
	}
	switch {
	case len(c.IDs) > 0 && (c.Archived || c.Name != ""):
		return errors.New("give version IDs or --archived, not both")
	case len(c.IDs) == 0 && !c.Archived:
		return errors.New("give the version IDs (instruction_id) to delete, or --archived with --name")
	case c.Archived && c.Name == "":
		return errors.New("--archived needs --name, so it never means every archived version at once")
	}
	// Only exact versions: a name or identifier resolves to whichever
	// version is latest, too easy to get wrong for a delete.
	var bad []string
	for _, id := range c.IDs {
		if !uuidRE.MatchString(id) {
			bad = append(bad, fmt.Sprintf("%q", id))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s: not a version ID (instruction_id); delete takes those only, as smart-software list shows them", strings.Join(bad, ", "))
	}

	api, err := app.API()
	if err != nil {
		return err
	}
	org, err := app.OrgID()
	if err != nil {
		return err
	}
	ids := c.IDs
	if c.Archived {
		if ids, err = c.archivedIDs(app); err != nil {
			return err
		}
	}
	// Fetch every version before touching any, so an unknown ID stops the
	// run with nothing deleted. Backups are of the version as GET returns it.
	var versions []*addigy.SmartSoftware
	var unknown []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		s, err := api.SmartSoftware(app.Ctx, org, id)
		var apiErr *addigy.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			unknown = append(unknown, id)
			continue
		}
		if err != nil {
			return err
		}
		versions = append(versions, s)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("nothing was deleted: no smart software with instruction id %s", strings.Join(unknown, ", "))
	}
	if len(versions) == 0 {
		if app.json() {
			return output.JSON(app.Out, deleteResult{DryRun: c.DryRun, Versions: []deletedVersion{}})
		}
		fmt.Fprintf(app.Out, "No archived versions of items named like %q; nothing to delete.\n", c.Name)
		return nil
	}
	slices.SortStableFunc(versions, func(a, b *addigy.SmartSoftware) int {
		if n := cmpString(a.BaseIdentifier, b.BaseIdentifier); n != 0 {
			return n
		}
		return cmpVersion(swVersion(*a), swVersion(*b))
	})

	result := deleteResult{DryRun: c.DryRun, Versions: make([]deletedVersion, len(versions))}
	now := time.Now()
	dir := ""
	if !c.NoBackup {
		if dir, err = backupDir(app, c.BackupDir); err != nil {
			return err
		}
	}
	preview := app.Out
	if app.json() {
		preview = app.Err
	}
	for i, s := range versions {
		v := &result.Versions[i]
		*v = deletedVersion{Identifier: s.Identifier, Name: s.BaseIdentifier, Version: swVersion(*s), InstructionID: s.InstructionID, Downloads: []resultFile{}}
		for _, d := range s.Downloads {
			v.Downloads = append(v.Downloads, resultFile{ID: d.ID, Filename: d.Filename, MD5: d.MD5Hash})
		}
		if dir != "" {
			v.Backup = backupPath(dir, s, now)
		}
		if i > 0 {
			fmt.Fprintln(preview)
		}
		printDeletePreview(preview, s, v.Backup)
	}
	// Addigy's API doesn't show assignments reliably, so this can't be
	// checked; deleting an assigned version drops it from its policies.
	fmt.Fprintln(preview, "\nwarning: a version that is assigned to policies is removed from them by")
	fmt.Fprintln(preview, "         Addigy without asking, and the backup does not record them.")

	if c.DryRun {
		if app.json() {
			return output.JSON(app.Out, result)
		}
		fmt.Fprintln(app.Out, "\nDry run: nothing was backed up or deleted.")
		return nil
	}
	if !c.Yes {
		question := fmt.Sprintf("Delete %s?", plural(len(versions), "version"))
		if len(versions) == 1 {
			s := versions[0]
			question = fmt.Sprintf("Delete %s %s (instruction_id %s)?", s.BaseIdentifier, swVersion(*s), s.InstructionID)
		}
		ok, err := app.confirm(question)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled; nothing was deleted")
		}
	}

	var deleted int
	for i, s := range versions {
		v := &result.Versions[i]
		err := deleteVersion(app, api, org, s, v.Backup, now)
		if err == nil {
			v.Deleted = true
			deleted++
			continue
		}
		v.Error = err.Error()
		v.Backup = "" // never written, or removed with the failed delete
		fmt.Fprintf(app.Err, "not deleted: %s %s (instruction_id %s): %s\n", s.BaseIdentifier, swVersion(*s), s.InstructionID, v.Error)
		// Without its backup no version may go, and an API key that may not
		// delete can delete none: don't try the rest.
		var apiErr *addigy.APIError
		denied := errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden)
		if errors.Is(err, errNoBackup) || denied {
			rest := result.Versions[i+1:]
			for j := range rest {
				rest[j].Error, rest[j].Backup = "not attempted", ""
			}
			if len(rest) > 0 {
				fmt.Fprintf(app.Err, "stopped: %s not attempted\n", plural(len(rest), "version"))
			}
			break
		}
	}

	if app.json() {
		if err := output.JSON(app.Out, result); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(app.Out)
		for _, v := range result.Versions {
			if v.Deleted {
				fmt.Fprintf(app.Out, "Deleted %s %s (instruction_id %s).\n", v.Name, v.Version, v.InstructionID)
				if v.Backup != "" {
					fmt.Fprintf(app.Out, "Backup: %s\n", v.Backup)
				}
			}
		}
		if len(versions) > 1 {
			fmt.Fprintf(app.Out, "Deleted %d of %s.\n", deleted, plural(len(versions), "version"))
		}
	}
	if failed := len(versions) - deleted; failed > 0 {
		if len(versions) == 1 {
			return errors.New(result.Versions[0].Error)
		}
		return fmt.Errorf("%s could not be deleted (see above)", plural(failed, "version"))
	}
	return nil
}

// errNoBackup marks a version left alone because its backup failed.
var errNoBackup = errors.New("could not write the backup, so it was not deleted (--no-backup skips it)")

// deleteVersion backs s up to backup (unless that is empty), then deletes it.
// When the delete fails the backup is removed again: a backup claims a
// delete happened.
func deleteVersion(app *App, api *addigy.API, org string, s *addigy.SmartSoftware, backup string, now time.Time) error {
	if backup != "" {
		if err := writeBackup(backup, org, s, now); err != nil {
			return fmt.Errorf("%w: %v", errNoBackup, err)
		}
	}
	err := api.DeleteSmartSoftware(app.Ctx, org, s.InstructionID)
	if err == nil || backup == "" {
		return err
	}
	if rmErr := os.Remove(backup); rmErr != nil {
		return fmt.Errorf("%w; its backup could not be removed: %v", err, rmErr)
	}
	return fmt.Errorf("%w; its backup was removed", err)
}

// archivedIDs are the instruction IDs of the archived versions of the items
// whose name contains --name.
func (c *SmartSoftwareDeleteCmd) archivedIDs(app *App) ([]string, error) {
	archived := true
	items, err := allSmartSoftware(app, addigy.SmartSoftwareQuery{NameContains: c.Name, Archived: &archived})
	if err != nil {
		return nil, err
	}
	// Addigy filters already; check again, as a delete must not rely on a
	// loose server-side match.
	needle := strings.ToLower(c.Name)
	var ids []string
	for _, s := range items {
		if s.Archived && strings.Contains(strings.ToLower(s.BaseIdentifier), needle) {
			ids = append(ids, s.InstructionID)
		}
	}
	return ids, nil
}

// backupDir is the --backup-dir flag, else the config file's "backup_dir",
// else "backups" in addigyctl's config directory.
func backupDir(app *App, flag string) (string, error) {
	switch {
	case flag != "":
		return expandHome(flag), nil
	case app.Cfg.BackupDir != "":
		return expandHome(app.Cfg.BackupDir), nil
	}
	dir, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("finding the default backup folder (set --backup-dir or \"backup_dir\" in the config file): %w", err)
	}
	return filepath.Join(dir, "backups"), nil
}

type deleteResult struct {
	DryRun   bool             `json:"dry_run"`
	Versions []deletedVersion `json:"versions"`
}

type deletedVersion struct {
	Identifier    string       `json:"identifier"`
	Name          string       `json:"name"`
	Version       string       `json:"version"`
	InstructionID string       `json:"instruction_id"`
	Backup        string       `json:"backup,omitempty"` // the backup file; empty with --no-backup or when the version wasn't deleted
	Downloads     []resultFile `json:"downloads"`        // still in Addigy's file storage after the delete
	Deleted       bool         `json:"deleted"`
	Error         string       `json:"error,omitempty"`
}

func printDeletePreview(w io.Writer, s *addigy.SmartSoftware, backup string) {
	fmt.Fprintf(w, "Delete %s %s (instruction_id %s)\n", s.BaseIdentifier, swVersion(*s), s.InstructionID)
	fmt.Fprintf(w, "  identifier: %s\n", s.Identifier)
	if s.Archived {
		fmt.Fprintln(w, "  archived:   yes")
	}
	if len(s.Downloads) == 0 {
		fmt.Fprintln(w, "  downloads:  none")
	} else {
		fmt.Fprintln(w, "  downloads (they stay in Addigy's file storage):")
		for _, d := range s.Downloads {
			fmt.Fprintf(w, "    %s  %s  (md5 %s)\n", d.ID, d.Filename, d.MD5Hash)
		}
	}
	if backup == "" {
		fmt.Fprintln(w, "  backup:     none (--no-backup)")
	} else {
		fmt.Fprintf(w, "  backup:     %s\n", backup)
	}
}

// ---- backups ------------------------------------------------------------------

// backupPath is <dir>/<item>/<version>-<instruction_id>-<UTC time>.json.
func backupPath(dir string, s *addigy.SmartSoftware, now time.Time) string {
	item := swfolder.FolderName(s.BaseIdentifier)
	if item == "" {
		item = "unnamed"
	}
	var parts []string
	if v := swfolder.FolderName(swVersion(*s)); v != "" {
		parts = append(parts, v)
	}
	parts = append(parts, s.InstructionID, now.UTC().Format("20060102T150405Z"))
	return filepath.Join(dir, item, strings.Join(parts, "-")+".json")
}

// backup is a backup file: the version exactly as Addigy returned it, which
// includes its downloads' IDs, names, sizes and MD5s (not the files).
type backup struct {
	Format         int             `json:"addigyctl_backup"`
	BackedUpAt     string          `json:"backed_up_at"`
	OrganizationID string          `json:"organization_id"`
	SmartSoftware  json.RawMessage `json:"smart_software"`
}

// writeBackup writes s's backup to path. It never overwrites a file. Backups
// are private: their scripts may hold license keys or tokens.
func writeBackup(path, orgID string, s *addigy.SmartSoftware, now time.Time) error {
	b, err := json.Marshal(backup{Format: 1, BackedUpAt: now.UTC().Format(time.RFC3339), OrganizationID: orgID, SmartSoftware: s.Raw})
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	return writePrivate(path, buf.Bytes())
}

// writePrivate writes a new file only its owner can read, creating its
// folder the same way. It never overwrites a file.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
