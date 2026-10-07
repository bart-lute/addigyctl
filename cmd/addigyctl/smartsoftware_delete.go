package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/config"
	"github.com/ginkio/addigyctl/internal/output"
	"github.com/ginkio/addigyctl/internal/swfolder"
)

// ---- delete -----------------------------------------------------------------

type SmartSoftwareDeleteCmd struct {
	ID        string `arg:"" name:"instruction-id" help:"Version ID (instruction_id) of the version to delete, as smart-software list shows it."`
	DryRun    bool   `name:"dry-run" help:"Show what would be deleted and where the backup would go, without doing either."`
	Yes       bool   `help:"Delete without asking for confirmation."`
	NoBackup  bool   `name:"no-backup" help:"Delete without writing a backup first."`
	BackupDir string `name:"backup-dir" placeholder:"DIR" help:"Folder for backups (default: \"backup_dir\" in the config file, else <user config dir>/addigyctl/backups)."`
}

func (c *SmartSoftwareDeleteCmd) Run(app *App) error {
	if err := app.noCSV("smart-software delete"); err != nil {
		return err
	}
	// Only an exact version: a name or identifier resolves to whichever
	// version is latest, too easy to get wrong for a delete.
	if !uuidRE.MatchString(c.ID) {
		return fmt.Errorf("%q is not a version ID (instruction_id); delete takes exactly one, as smart-software list shows it", c.ID)
	}
	api, err := app.API()
	if err != nil {
		return err
	}
	org, err := app.OrgID()
	if err != nil {
		return err
	}
	s, err := api.SmartSoftware(app.Ctx, org, c.ID)
	if err != nil {
		return err
	}

	var backup string
	if !c.NoBackup {
		dir, err := backupDir(app, c.BackupDir)
		if err != nil {
			return err
		}
		backup = backupPath(dir, s, time.Now())
	}

	preview := app.Out
	if app.json() {
		preview = app.Err
	}
	printDeletePreview(preview, s, backup)

	result := deleteResult{
		DryRun:        c.DryRun,
		Identifier:    s.Identifier,
		Name:          s.BaseIdentifier,
		Version:       swVersion(*s),
		InstructionID: s.InstructionID,
		Backup:        backup,
		Downloads:     []resultFile{},
	}
	for _, d := range s.Downloads {
		result.Downloads = append(result.Downloads, resultFile{ID: d.ID, Filename: d.Filename, MD5: d.MD5Hash})
	}
	if c.DryRun {
		if app.json() {
			return output.JSON(app.Out, result)
		}
		fmt.Fprintln(app.Out, "\nDry run: nothing was backed up or deleted.")
		return nil
	}
	if !c.Yes {
		ok, err := app.confirm(fmt.Sprintf("Delete %s %s (instruction_id %s)?", s.BaseIdentifier, swVersion(*s), s.InstructionID))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled; nothing was deleted")
		}
	}

	// Back up only now, so a cancelled delete leaves no backup behind.
	if backup != "" {
		if err := writeBackup(backup, org, s, time.Now()); err != nil {
			return fmt.Errorf("could not write the backup, so nothing was deleted (--no-backup skips it): %w", err)
		}
	}
	if err := api.DeleteSmartSoftware(app.Ctx, org, s.InstructionID); err != nil {
		// A backup claims a delete happened: drop it.
		if backup == "" {
			return err
		}
		if rmErr := os.Remove(backup); rmErr != nil {
			return fmt.Errorf("%w; nothing was deleted, but its backup could not be removed: %v", err, rmErr)
		}
		return fmt.Errorf("%w; nothing was deleted and its backup was removed", err)
	}

	if app.json() {
		return output.JSON(app.Out, result)
	}
	fmt.Fprintf(app.Out, "\nDeleted %s %s (instruction_id %s).\n", s.BaseIdentifier, swVersion(*s), s.InstructionID)
	if backup != "" {
		fmt.Fprintf(app.Out, "Backup: %s\n", backup)
	}
	return nil
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
	DryRun        bool         `json:"dry_run"`
	Identifier    string       `json:"identifier"`
	Name          string       `json:"name"`
	Version       string       `json:"version"`
	InstructionID string       `json:"instruction_id"`
	Backup        string       `json:"backup,omitempty"` // the backup file; empty with --no-backup
	Downloads     []resultFile `json:"downloads"`        // still in Addigy's file storage after the delete
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
	// Addigy's API doesn't show assignments reliably, so this can't be
	// checked; deleting an assigned version drops it from its policies.
	fmt.Fprintln(w, "warning: if this version is assigned to policies, Addigy removes it from")
	fmt.Fprintln(w, "         them without asking, and the backup does not record them.")
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
