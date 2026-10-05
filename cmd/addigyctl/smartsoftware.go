package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
	"github.com/ginkio/addigyctl/internal/swfolder"
)

type SmartSoftwareCmd struct {
	List       SmartSoftwareListCmd       `cmd:"" help:"List Smart Software, one row per version."`
	Get        SmartSoftwareGetCmd        `cmd:"" help:"Show one Smart Software version as JSON."`
	Export     SmartSoftwareExportCmd     `cmd:"" help:"Write a Smart Software item to an item folder (item.yaml plus scripts)."`
	NewVersion SmartSoftwareNewVersionCmd `cmd:"" name:"new-version" help:"Publish a new version of an item from its item folder."`
}

// ---- list -------------------------------------------------------------------

type SmartSoftwareListCmd struct {
	Name     string `help:"Only items whose name contains this text."`
	Archived bool   `help:"Include archived versions."`
}

func (c *SmartSoftwareListCmd) Run(app *App) error {
	q := addigy.SmartSoftwareQuery{NameContains: c.Name}
	if !c.Archived {
		active := false
		q.Archived = &active
	}
	items, err := allSmartSoftware(app, q)
	if err != nil {
		return err
	}
	slices.SortStableFunc(items, func(a, b addigy.SmartSoftware) int {
		if n := cmpString(a.BaseIdentifier, b.BaseIdentifier); n != 0 {
			return n
		}
		return cmpVersion(swVersion(a), swVersion(b))
	})

	if app.json() {
		raws := make([]json.RawMessage, len(items))
		for i, s := range items {
			raws[i] = s.Raw
		}
		return output.JSON(app.Out, raws)
	}

	headers := []string{"NAME", "VERSION", "CATEGORY", "ID", "IDENTIFIER"}
	if c.Archived {
		headers = slices.Insert(headers, 3, "ARCHIVED")
	}
	rows := make([][]string, 0, len(items))
	for _, s := range items {
		row := []string{app.cell(s.BaseIdentifier), app.cell(swVersion(s)), app.cell(s.Category), app.cell(s.InstructionID), app.cell(s.Identifier)}
		if c.Archived {
			row = slices.Insert(row, 3, app.cell(s.Archived))
		}
		rows = append(rows, row)
	}
	if err := output.Rows(app.Out, app.Format(), headers, rows, app.borders()); err != nil {
		return err
	}
	app.footer("%d versions", len(items))
	return nil
}

// ---- get --------------------------------------------------------------------

type SmartSoftwareGetCmd struct {
	Ref string `arg:"" name:"ref" help:"Version ID (instruction_id), or an item's identifier or name for its latest version."`
}

func (c *SmartSoftwareGetCmd) Run(app *App) error {
	if err := app.noCSV("smart-software get"); err != nil {
		return err
	}
	s, err := resolveSmartSoftware(app, c.Ref)
	if err != nil {
		return err
	}
	return output.JSON(app.Out, s.Raw)
}

// ---- export -----------------------------------------------------------------

type SmartSoftwareExportCmd struct {
	Ref string `arg:"" name:"ref" help:"Version ID (instruction_id), or an item's identifier or name for its latest version."`
	itemTarget
	Force        bool `help:"Overwrite the item folder's existing addigyctl files (only ever for the same item)."`
	Placeholders bool `help:"Replace the exported version number with {{.Version}} in the scripts and item.yaml. Plain text matching: review the result."`
}

func (c *SmartSoftwareExportCmd) Run(app *App) error {
	if err := app.noCSV("smart-software export"); err != nil {
		return err
	}
	s, err := resolveSmartSoftware(app, c.Ref)
	if err != nil {
		return err
	}
	// Without --dir or --item, the folder is named after the item.
	dir, err := c.pathOr(app, swfolder.FolderName(s.BaseIdentifier))
	if err != nil {
		return err
	}
	folder, err := folderFromSmartSoftware(s)
	if err != nil {
		return err
	}
	// With --placeholders, downloads named after the version become
	// version_downloads patterns: "helloworld-1.0.0" -> "helloworld-{{.Version}}".
	if c.Placeholders {
		for _, d := range s.Downloads {
			folder.VersionDownloads = append(folder.VersionDownloads, d.Filename)
		}
	}
	// The folder is a template; keep any "{{" in Addigy's content literal.
	folder = folder.Literal()
	var replaced map[string]int
	if c.Placeholders {
		folder, replaced = folder.WithPlaceholders(swVersion(*s))
		// A name without the version can't follow it: leave it out.
		folder.VersionDownloads = slices.DeleteFunc(folder.VersionDownloads, func(p string) bool {
			return !strings.Contains(p, "{{.Version}}")
		})
	}
	if err := swfolder.Write(dir, folder, exportHeader(s, folder.VersionDownloads), c.Force); err != nil {
		return err
	}
	if err := writeStateFor(dir, s); err != nil {
		return err
	}

	files := []string{swfolder.ItemFile}
	for _, sc := range []struct{ name, body string }{
		{swfolder.InstallFile, folder.InstallScript},
		{swfolder.ConditionFile, folder.Condition},
		{swfolder.RemoveFile, folder.RemoveScript},
	} {
		if sc.body != "" {
			files = append(files, sc.name)
		}
	}
	if app.json() {
		return output.JSON(app.Out, struct {
			Dir           string         `json:"dir"`
			Identifier    string         `json:"identifier"`
			InstructionID string         `json:"instruction_id"`
			Version       string         `json:"version"`
			Files         []string       `json:"files"`
			Placeholders  map[string]int `json:"placeholders,omitempty"` // with --placeholders: replacements per file
		}{dir, s.Identifier, s.InstructionID, swVersion(*s), files, replaced})
	}
	fmt.Fprintf(app.Out, "Exported %s %s to %s (%s)\n", s.BaseIdentifier, swVersion(*s), dir, strings.Join(files, ", "))
	if c.Placeholders {
		if len(replaced) == 0 {
			fmt.Fprintf(app.Out, "Version %s does not occur in the scripts or %s; nothing replaced.\n", swVersion(*s), swfolder.ItemFile)
		} else {
			var per []string
			for _, name := range swfolder.OwnedFiles() {
				if n := replaced[name]; n > 0 {
					per = append(per, fmt.Sprintf("%s (%d)", name, n))
				}
			}
			fmt.Fprintf(app.Out, "Replaced %s with {{.Version}} in %s. Review these: not every match need be this item's version.\n",
				swVersion(*s), strings.Join(per, ", "))
		}
	}
	for _, p := range folder.VersionDownloads {
		fmt.Fprintf(app.Out, "version_downloads: %s\n", p)
	}
	if n := len(s.Downloads) - len(folder.VersionDownloads); n > 0 {
		fmt.Fprintf(app.Out, "%d download(s) are only listed in the %s header: add them to downloads if every version needs them, or as a pattern to version_downloads.\n",
			n, swfolder.ItemFile)
	}
	return nil
}

// folderFromSmartSoftware maps one version, as returned by the API, to an
// item folder. The version's own downloads are left out: there is no telling
// which of them every version needs and which is this version's installer,
// and copying them would silently carry the installer into new versions.
func folderFromSmartSoftware(s *addigy.SmartSoftware) (swfolder.Folder, error) {
	var f swfolder.Folder
	if err := json.Unmarshal(s.Raw, &f.Item); err != nil {
		return swfolder.Folder{}, fmt.Errorf("decoding smart software: %w", err)
	}
	var scripts struct {
		Install   string `json:"installation_script"`
		Condition string `json:"condition"`
		Remove    string `json:"remove_script"`
	}
	if err := json.Unmarshal(s.Raw, &scripts); err != nil {
		return swfolder.Folder{}, fmt.Errorf("decoding smart software: %w", err)
	}
	f.InstallScript, f.Condition, f.RemoveScript = scripts.Install, scripts.Condition, scripts.Remove

	f.Downloads = []swfolder.Download{}
	f.VersionDownloads = []string{}
	if f.Profiles == nil {
		f.Profiles = []any{}
	}
	if f.PredefinedConditions == nil {
		f.PredefinedConditions = map[string]any{}
	}
	return f, nil
}

func exportHeader(s *addigy.SmartSoftware, patterns []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Exported by addigyctl from %s, version %s (instruction_id %s).\n", s.BaseIdentifier, swVersion(*s), s.InstructionID)
	if len(s.Downloads) == 0 {
		b.WriteString("That version has no downloads.\n")
		return b.String()
	}
	b.WriteString("That version's downloads. Files every version needs go in downloads (by ID);\n")
	b.WriteString("files each version needs go in version_downloads (by name pattern):\n")
	for _, d := range s.Downloads {
		note := "not copied"
		if len(patterns) > 0 && slices.ContainsFunc(patterns, func(p string) bool {
			return strings.ReplaceAll(p, "{{.Version}}", swVersion(*s)) == d.Filename
		}) {
			note = "→ version_downloads"
		}
		fmt.Fprintf(&b, "  %s  %s  (%s)\n", d.ID, d.Filename, note)
	}
	return b.String()
}

// ---- item folder target ------------------------------------------------------

// itemTarget is the --dir/--item pair that points a command at an item folder.
type itemTarget struct {
	Dir  string `xor:"target" placeholder:"PATH" help:"Item folder path."`
	Item string `xor:"target" placeholder:"NAME" help:"Item folder name inside the software root (--software-root, ADDIGYCTL_SOFTWARE_ROOT or \"software_root\" in the config file). Export defaults to one derived from the item's name."`
}

func (t itemTarget) path(app *App) (string, error) {
	if t.Dir == "" && t.Item == "" {
		return "", errors.New("one of --dir or --item is required")
	}
	return t.pathOr(app, "")
}

// pathOr is path, but with neither --dir nor --item it uses the folder
// defaultName inside the software root.
func (t itemTarget) pathOr(app *App, defaultName string) (string, error) {
	if t.Dir != "" {
		return expandHome(t.Dir), nil
	}
	name := t.Item
	if name == "" {
		name = defaultName
	}
	if app.G.SoftwareRoot == "" {
		return "", errors.New("no software root to put the item folder in: set --software-root, ADDIGYCTL_SOFTWARE_ROOT or \"software_root\" in the config file, or use --dir")
	}
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("item folder name %q must be a folder name, not a path (use --dir for paths)", name)
	}
	return filepath.Join(app.G.SoftwareRoot, name), nil
}

// ---- resolving a version -----------------------------------------------------

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// resolveSmartSoftware finds the version ref names. A bare UUID is a version's
// instruction_id. Anything else is an item's identifier ("<name>-<uuid>",
// shared by its versions) or else its name (base_identifier, compared
// case-insensitively); both resolve to the item's latest version, archived
// or not.
func resolveSmartSoftware(app *App, ref string) (*addigy.SmartSoftware, error) {
	api, err := app.API()
	if err != nil {
		return nil, err
	}
	if uuidRE.MatchString(ref) {
		org, err := app.OrgID()
		if err != nil {
			return nil, err
		}
		return api.SmartSoftware(app.Ctx, org, ref)
	}

	versions, err := allSmartSoftware(app, addigy.SmartSoftwareQuery{Identifier: ref})
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		candidates, err := allSmartSoftware(app, addigy.SmartSoftwareQuery{NameContains: ref})
		if err != nil {
			return nil, err
		}
		for _, s := range candidates {
			if strings.EqualFold(s.BaseIdentifier, ref) {
				versions = append(versions, s)
			}
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no smart software with identifier or name %q", ref)
	}

	ids := map[string]bool{}
	for _, s := range versions {
		ids[s.Identifier] = true
	}
	if len(ids) > 1 {
		return nil, fmt.Errorf("%q names %d items; use one of their identifiers: %s",
			ref, len(ids), strings.Join(slices.Sorted(maps.Keys(ids)), ", "))
	}
	latest := slices.MaxFunc(versions, func(a, b addigy.SmartSoftware) int {
		return cmpVersion(swVersion(a), swVersion(b))
	})
	return &latest, nil
}

// allSmartSoftware runs q across every page.
func allSmartSoftware(app *App, q addigy.SmartSoftwareQuery) ([]addigy.SmartSoftware, error) {
	api, err := app.API()
	if err != nil {
		return nil, err
	}
	q.PerPage = 100 // the endpoint's maximum
	var all []addigy.SmartSoftware
	for q.Page = 1; ; q.Page++ {
		pg, err := api.SearchSmartSoftware(app.Ctx, q)
		if err != nil {
			return nil, err
		}
		all = append(all, pg.Items...)
		if len(pg.Items) == 0 || q.Page >= pg.Metadata.PageCount {
			return all, nil
		}
	}
}

// swVersion is a version's version string. Addigy's spec leaves its type
// open; it is a string in practice.
func swVersion(s addigy.SmartSoftware) string {
	if s.Version == nil {
		return ""
	}
	return fmt.Sprint(s.Version)
}
