package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

// ---- restore ----------------------------------------------------------------

type SmartSoftwareRestoreCmd struct {
	Refs      []string `arg:"" name:"backup" help:"What to restore: the deleted version's ID (instruction_id), as smart-software backups lists it, or the path of a backup file."`
	DryRun    bool     `name:"dry-run" help:"Show what would be restored, without restoring it."`
	Yes       bool     `help:"Restore without asking for confirmation."`
	BackupDir string   `name:"backup-dir" placeholder:"DIR" help:"Folder to find backups by ID in (default: \"backup_dir\" in the config file, else <user config dir>/addigyctl/backups)."`
}

// restoreFields are the settings a restore takes from a backup: what a
// create or new-version request accepts and the user set, not what Addigy
// manages itself (label, provider, organization_id, ...).
var restoreFields = []string{
	"identifier", "base_identifier", "version", "category", "description", "priority",
	"installation_script", "condition", "remove_script", "run_on_success", "status_on_skipped",
	"predefined_conditions", "profiles",
}

// restorePlan is one backup checked against Addigy and ready to restore.
type restorePlan struct {
	Path      string
	Version   addigy.SmartSoftware
	Body      map[string]any
	Downloads []restoreFile
	Icon      *restoreFile // an uploaded icon; nil for none or a web icon
	Target    string       // instruction ID of a version to add it next to; empty creates the item
	TargetVer string
}

// restoreFile is a file the backed-up version used, and the upload that
// serves it now.
type restoreFile struct {
	OldID, ID, Filename, MD5 string
	Reuploaded               bool // the old upload is gone; ID has the same content
}

func (c *SmartSoftwareRestoreCmd) Run(app *App) error {
	if err := app.noCSV("smart-software restore"); err != nil {
		return err
	}
	paths, err := c.backupPaths(app)
	if err != nil {
		return err
	}
	api, err := app.API()
	if err != nil {
		return err
	}
	org, err := app.OrgID()
	if err != nil {
		return err
	}
	// Check every backup before restoring any.
	var plans []*restorePlan
	var problems []string
	seen := map[string]string{} // identifier + version -> backup
	for _, path := range paths {
		p, err := planRestore(app, api, org, path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		key := p.Version.Identifier + "\x00" + swVersion(p.Version)
		if first, ok := seen[key]; ok {
			problems = append(problems, fmt.Sprintf("%s: the same version as %s", path, first))
			continue
		}
		seen[key] = path
		plans = append(plans, p)
	}
	if len(problems) > 0 {
		return fmt.Errorf("nothing was restored:\n  %s", strings.Join(problems, "\n  "))
	}

	preview := app.Out
	if app.json() {
		preview = app.Err
	}
	result := restoreResult{DryRun: c.DryRun, Versions: make([]restoredVersion, len(plans))}
	for i, p := range plans {
		result.Versions[i] = restoredVersion{Backup: p.Path, Identifier: p.Version.Identifier, Name: p.Version.BaseIdentifier, Version: swVersion(p.Version)}
		if i > 0 {
			fmt.Fprintln(preview)
		}
		printRestorePreview(preview, p)
	}
	fmt.Fprintln(preview, "\nRestored versions come back archived, with a new instruction_id and no policy assignments.")
	if c.DryRun {
		if app.json() {
			return output.JSON(app.Out, result)
		}
		fmt.Fprintln(app.Out, "\nDry run: nothing was restored.")
		return nil
	}
	if !c.Yes {
		question := fmt.Sprintf("Restore %s?", plural(len(plans), "version"))
		if len(plans) == 1 {
			question = fmt.Sprintf("Restore %s %s?", plans[0].Version.BaseIdentifier, swVersion(plans[0].Version))
		}
		ok, err := app.confirm(question)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled; nothing was restored")
		}
	}

	created := map[string]string{} // backed-up identifier -> a version created for it in this run
	var restored int
	for i, p := range plans {
		r := &result.Versions[i]
		target := p.Target
		if target == "" {
			target = created[p.Version.Identifier]
		}
		s, err := restoreVersion(app, api, org, p, target)
		if err == nil {
			restored++
			r.Restored, r.InstructionID, r.Archived = true, s.InstructionID, s.Archived
			r.Identifier = s.Identifier
			created[p.Version.Identifier] = s.InstructionID
			if !s.Archived {
				fmt.Fprintf(app.Err, "warning: Addigy did not keep %s %s archived; archive it in the Addigy UI.\n", s.BaseIdentifier, swVersion(*s))
			}
			continue
		}
		r.Error = err.Error()
		fmt.Fprintf(app.Err, "not restored: %s %s (%s): %v\n", p.Version.BaseIdentifier, swVersion(p.Version), p.Path, err)
		var apiErr *addigy.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
			rest := result.Versions[i+1:]
			for j := range rest {
				rest[j].Error = "not attempted"
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
		for _, r := range result.Versions {
			if r.Restored {
				fmt.Fprintf(app.Out, "Restored %s %s (instruction_id %s).\n", r.Name, r.Version, r.InstructionID)
			}
		}
		if len(plans) > 1 {
			fmt.Fprintf(app.Out, "Restored %d of %s.\n", restored, plural(len(plans), "version"))
		}
	}
	if failed := len(plans) - restored; failed > 0 {
		if len(plans) == 1 {
			return errors.New(result.Versions[0].Error)
		}
		return fmt.Errorf("%s could not be restored (see above)", plural(failed, "version"))
	}
	return nil
}

// backupPaths turns the arguments into backup files: a UUID is a deleted
// version's ID, looked up in the backup folder (the newest backup of it);
// anything else is a file path.
func (c *SmartSoftwareRestoreCmd) backupPaths(app *App) ([]string, error) {
	var paths, unknown []string
	var backups []*backupFile
	var dir string
	for _, ref := range c.Refs {
		if !uuidRE.MatchString(ref) {
			if _, err := os.Stat(ref); err != nil {
				return nil, err
			}
			paths = append(paths, ref)
			continue
		}
		if backups == nil {
			var err error
			if dir, err = backupDir(app, c.BackupDir); err != nil {
				return nil, err
			}
			if backups, err = listBackups(dir); err != nil {
				return nil, err
			}
		}
		var newest *backupFile
		n := 0
		for _, b := range backups {
			if strings.EqualFold(b.Version.InstructionID, ref) {
				n++
				if newest == nil || b.BackedUpAt > newest.BackedUpAt {
					newest = b
				}
			}
		}
		if newest == nil {
			unknown = append(unknown, ref)
			continue
		}
		if n > 1 {
			fmt.Fprintf(app.Err, "note: %d backups of %s; using the newest, from %s\n", n, ref, newest.BackedUpAt)
		}
		paths = append(paths, newest.Path)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("no backup of %s in %s (smart-software backups lists them)", strings.Join(unknown, ", "), dir)
	}
	return paths, nil
}

// ---- backup files -------------------------------------------------------------

// backupFile is a backup file as read back.
type backupFile struct {
	backup
	Path    string
	Version addigy.SmartSoftware
	Raw     map[string]any
}

// neededFile is a file a backed-up version used: a download, or its
// uploaded icon.
type neededFile struct {
	ID, Filename, MD5 string
	Icon              bool
}

func readBackup(path string) (*backupFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b := &backupFile{Path: path}
	if err := json.Unmarshal(data, &b.backup); err != nil || b.Format != 1 || len(b.SmartSoftware) == 0 {
		return nil, errors.New("not a backup written by smart-software delete")
	}
	if err := json.Unmarshal(b.SmartSoftware, &b.Version); err != nil {
		return nil, fmt.Errorf("decoding the backed-up version: %w", err)
	}
	if err := json.Unmarshal(b.SmartSoftware, &b.Raw); err != nil {
		return nil, err
	}
	return b, nil
}

// listBackups reads every backup in dir (<dir>/<item>/*.json). Other files,
// such as files delete's deletion logs, are skipped.
func listBackups(dir string) ([]*backupFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*backupFile
	for _, p := range paths {
		if b, err := readBackup(p); err == nil {
			out = append(out, b)
		}
	}
	return out, nil
}

// icon is the backed-up version's icon: a web URL or an uploaded file.
func (b *backupFile) icon() (id, provider, filename, md5 string) {
	var ic struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Filename string `json:"filename"`
		MD5      string `json:"md5_hash"`
	}
	if v, ok := b.Raw["software_icon"]; ok && v != nil {
		j, _ := json.Marshal(v)
		json.Unmarshal(j, &ic)
	}
	return ic.ID, ic.Provider, ic.Filename, ic.MD5
}

// neededFiles are the uploads the backed-up version used.
func (b *backupFile) neededFiles() []neededFile {
	var out []neededFile
	for _, d := range b.Version.Downloads {
		out = append(out, neededFile{ID: d.ID, Filename: d.Filename, MD5: d.MD5Hash})
	}
	if id, provider, filename, md5 := b.icon(); id != "" && provider == "cloud-storage" {
		out = append(out, neededFile{ID: id, Filename: filename, MD5: md5, Icon: true})
	}
	return out
}

func (f neededFile) String() string {
	if f.Icon {
		return fmt.Sprintf("icon %s (md5 %s)", f.Filename, f.MD5)
	}
	return fmt.Sprintf("%s (md5 %s)", f.Filename, f.MD5)
}

// ---- planning -------------------------------------------------------------------

// planRestore reads a backup and checks it against Addigy: the organization,
// that the version doesn't exist (any more), and that its files are there.
func planRestore(app *App, api *addigy.API, org, path string) (*restorePlan, error) {
	b, err := readBackup(path)
	if err != nil {
		return nil, err
	}
	if b.OrganizationID != org {
		return nil, fmt.Errorf("backed up from organization %s, not this one (%s)", b.OrganizationID, org)
	}
	s := b.Version
	p := &restorePlan{Path: path, Version: s, Body: map[string]any{"archived": true}}
	for _, k := range restoreFields {
		if v, ok := b.Raw[k]; ok {
			p.Body[k] = v
		}
	}

	versions, err := allSmartSoftware(app, addigy.SmartSoftwareQuery{Identifier: s.Identifier})
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		if v.Identifier != s.Identifier {
			continue
		}
		if swVersion(v) == swVersion(s) {
			return nil, fmt.Errorf("%s %s is already in Addigy (instruction_id %s)", s.BaseIdentifier, swVersion(s), v.InstructionID)
		}
		if p.Target == "" || cmpVersion(swVersion(v), p.TargetVer) > 0 {
			p.Target, p.TargetVer = v.InstructionID, swVersion(v)
		}
	}

	var missing []string
	downloads := []map[string]string{}
	for _, n := range b.neededFiles() {
		f, err := findUpload(app, api, n.ID, n.MD5)
		if err != nil {
			return nil, err
		}
		if f == nil {
			missing = append(missing, n.String())
			continue
		}
		rf := restoreFile{OldID: n.ID, ID: f.ID, Filename: n.Filename, MD5: n.MD5, Reuploaded: f.ID != n.ID}
		if n.Icon {
			p.Icon = &rf
		} else {
			p.Downloads = append(p.Downloads, rf)
			downloads = append(downloads, map[string]string{"id": f.ID})
		}
	}
	p.Body["downloads"] = downloads
	if id, provider, _, _ := b.icon(); id != "" {
		if p.Icon != nil {
			id = p.Icon.ID
		}
		p.Body["software_icon"] = map[string]string{"id": id, "provider": provider}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s %s needs files that are no longer in Addigy; upload them again (same content) and retry: %s",
			s.BaseIdentifier, swVersion(s), strings.Join(missing, ", "))
	}
	return p, nil
}

// findUpload finds a backed-up file: the upload with its old ID, else the
// newest one with the same content (MD5), uploaded again after it was
// deleted. Nil when neither exists.
func findUpload(app *App, api *addigy.API, id, md5 string) (*addigy.File, error) {
	f, err := api.File(app.Ctx, id)
	var apiErr *addigy.APIError
	if err == nil {
		return f, nil
	}
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		return nil, err
	}
	if md5 == "" {
		return nil, nil
	}
	pg, err := api.SearchFiles(app.Ctx, addigy.FileQuery{MD5Hashes: []string{md5}, SortField: "created", Desc: true, PerPage: 100})
	if err != nil {
		return nil, err
	}
	for _, f := range pg.Items {
		if f.MD5Hash == md5 {
			return &f, nil
		}
	}
	return nil, nil
}

// restoreVersion creates the backed-up version: next to target, or as a new
// item when there is none. It returns the version as Addigy stores it.
func restoreVersion(app *App, api *addigy.API, org string, p *restorePlan, target string) (*addigy.SmartSoftware, error) {
	body, err := json.Marshal(p.Body)
	if err != nil {
		return nil, err
	}
	var s *addigy.SmartSoftware
	if target != "" {
		s, err = api.NewSmartSoftwareVersion(app.Ctx, org, target, body)
	} else {
		s, err = api.CreateSmartSoftware(app.Ctx, org, body)
	}
	if err != nil {
		return nil, err
	}
	// Read it back: the response may not reflect everything Addigy stored.
	if stored, err := api.SmartSoftware(app.Ctx, org, s.InstructionID); err == nil {
		s = stored
	}
	return s, nil
}

func printRestorePreview(w io.Writer, p *restorePlan) {
	fmt.Fprintf(w, "Restore %s %s from %s\n", p.Version.BaseIdentifier, swVersion(p.Version), p.Path)
	if p.Target != "" {
		fmt.Fprintf(w, "  as:         a new version of %s, next to %s\n", p.Version.Identifier, p.TargetVer)
	} else {
		fmt.Fprintf(w, "  as:         a new item; no version of %s is left\n", p.Version.Identifier)
	}
	if len(p.Downloads) == 0 {
		fmt.Fprintln(w, "  downloads:  none")
	} else {
		fmt.Fprintln(w, "  downloads:")
		for _, f := range p.Downloads {
			fmt.Fprintf(w, "    %s  %s%s\n", f.ID, f.Filename, reuploadNote(f))
		}
	}
	if p.Icon != nil {
		fmt.Fprintf(w, "  icon:       %s  %s%s\n", p.Icon.ID, p.Icon.Filename, reuploadNote(*p.Icon))
	}
}

func reuploadNote(f restoreFile) string {
	if f.Reuploaded {
		return fmt.Sprintf("  (uploaded again; was %s)", f.OldID)
	}
	return ""
}

type restoreResult struct {
	DryRun   bool              `json:"dry_run"`
	Versions []restoredVersion `json:"versions"`
}

type restoredVersion struct {
	Backup        string `json:"backup"`
	Identifier    string `json:"identifier"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	InstructionID string `json:"instruction_id,omitempty"` // the restored version's new ID
	Restored      bool   `json:"restored"`
	Archived      bool   `json:"archived"`
	Error         string `json:"error,omitempty"`
}
