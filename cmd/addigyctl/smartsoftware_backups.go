package main

import (
	"slices"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

// ---- backups ------------------------------------------------------------------

type SmartSoftwareBackupsCmd struct {
	Name      string `help:"Only items whose name contains this text (case-insensitive)."`
	BackupDir string `name:"backup-dir" placeholder:"DIR" help:"Folder holding the backups (default: \"backup_dir\" in the config file, else <user config dir>/addigyctl/backups)."`
}

// backupEntry is one backup as backups lists it.
type backupEntry struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Identifier    string   `json:"identifier"`
	InstructionID string   `json:"instruction_id"` // the deleted version's ID, which restore takes
	BackedUpAt    string   `json:"backed_up_at"`
	Path          string   `json:"path"`
	Status        string   `json:"status"`                 // ready, needs-upload, in-addigy or other-organization
	NeedsUpload   []string `json:"needs_upload,omitempty"` // with needs-upload: the files to upload again
}

func (c *SmartSoftwareBackupsCmd) Run(app *App) error {
	dir, err := backupDir(app, c.BackupDir)
	if err != nil {
		return err
	}
	all, err := listBackups(dir)
	if err != nil {
		return err
	}
	needle := strings.ToLower(c.Name)
	var backups []*backupFile
	for _, b := range all {
		if strings.Contains(strings.ToLower(b.Version.BaseIdentifier), needle) {
			backups = append(backups, b)
		}
	}

	entries := []backupEntry{}
	if len(backups) > 0 {
		// Two requests in all, however many backups: every file and every
		// version, then each backup is checked against those.
		org, err := app.OrgID()
		if err != nil {
			return err
		}
		files, err := allFiles(app, addigy.FileQuery{})
		if err != nil {
			return err
		}
		ids, md5s := map[string]bool{}, map[string]bool{}
		for _, f := range files {
			ids[f.ID], md5s[f.MD5Hash] = true, true
		}
		versions, err := allSmartSoftware(app, addigy.SmartSoftwareQuery{}) // archived and active
		if err != nil {
			return err
		}
		exists := map[string]bool{} // identifier + version
		for _, v := range versions {
			exists[v.Identifier+"\x00"+swVersion(v)] = true
		}
		for _, b := range backups {
			e := backupEntry{Name: b.Version.BaseIdentifier, Version: swVersion(b.Version), Identifier: b.Version.Identifier,
				InstructionID: b.Version.InstructionID, BackedUpAt: b.BackedUpAt, Path: b.Path, Status: "ready"}
			switch {
			case b.OrganizationID != org:
				e.Status = "other-organization"
			case exists[b.Version.Identifier+"\x00"+e.Version]:
				e.Status = "in-addigy"
			default:
				for _, n := range b.neededFiles() {
					if !ids[n.ID] && (n.MD5 == "" || !md5s[n.MD5]) {
						e.NeedsUpload = append(e.NeedsUpload, n.Filename)
					}
				}
				if len(e.NeedsUpload) > 0 {
					e.Status = "needs-upload"
				}
			}
			entries = append(entries, e)
		}
	}
	slices.SortStableFunc(entries, func(a, b backupEntry) int {
		if n := cmpString(a.Name, b.Name); n != 0 {
			return n
		}
		if n := cmpVersion(a.Version, b.Version); n != 0 {
			return n
		}
		return cmpString(a.BackedUpAt, b.BackedUpAt)
	})

	if app.json() {
		return output.JSON(app.Out, entries)
	}
	style, err := app.DateStyle()
	if err != nil {
		return err
	}
	var ready int
	rows := make([][]string, len(entries))
	for i, e := range entries {
		status := backupStatusText(e)
		if e.Status == "ready" {
			ready++
		}
		name, path := e.Name, e.Path
		if !app.csv() {
			name = output.Truncate(name, 40)
			status = output.Truncate(status, 60)
		}
		rows[i] = []string{app.cell(name), app.cell(e.Version), app.cell(e.InstructionID), app.cell(style.DateTime(e.BackedUpAt)), app.cell(status)}
		if app.csv() {
			rows[i] = append(rows[i], path)
		}
	}
	headers := []string{"NAME", "VERSION", "ID", "DELETED", "STATUS"}
	if app.csv() {
		headers = append(headers, "PATH")
	}
	if err := output.Rows(app.Out, app.Format(), headers, rows, app.borders()); err != nil {
		return err
	}
	app.footer("%s in %s, %d ready to restore (smart-software restore <ID>)", plural(len(entries), "backup"), dir, ready)
	return nil
}

func backupStatusText(e backupEntry) string {
	switch e.Status {
	case "ready":
		return "ready to restore"
	case "in-addigy":
		return "already in Addigy"
	case "other-organization":
		return "other organization"
	case "needs-upload":
		return "needs upload: " + strings.Join(e.NeedsUpload, ", ")
	}
	return e.Status
}
