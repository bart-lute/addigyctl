package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

type FilesCmd struct {
	List   FilesListCmd   `cmd:"" help:"List every uploaded file and what uses it."`
	Delete FilesDeleteCmd `cmd:"" help:"Delete uploaded files that nothing uses."`
	Find   FilesFindCmd   `cmd:"" help:"Find uploaded files (e.g. a Smart Software installer) by MD5 hash or name."`
}

type FilesFindCmd struct {
	MD5  []string `name:"md5" placeholder:"HASH" help:"MD5 hash of the file's content (repeatable), e.g. from 'md5 -q <file>'."`
	Name string   `help:"Search file names (case-insensitive). Addigy's search is loose: it also returns near matches, newest first."`
}

func (c *FilesFindCmd) Run(app *App) error {
	if len(c.MD5) == 0 && c.Name == "" {
		return errors.New("give --md5 or --name")
	}
	api, err := app.API()
	if err != nil {
		return err
	}
	pg, err := api.SearchFiles(app.Ctx, addigy.FileQuery{
		MD5Hashes:  c.MD5,
		SearchTerm: c.Name,
		SortField:  "created",
		Desc:       true,
		PerPage:    100, // the endpoint's maximum
	})
	if err != nil {
		return err
	}
	if app.json() {
		return output.JSON(app.Out, pg.Items)
	}
	style, err := app.DateStyle()
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(pg.Items))
	for _, f := range pg.Items {
		rows = append(rows, []string{
			app.cell(f.ID), app.cell(f.Filename), app.cell(f.Size), app.cell(style.DateTime(f.Created)), app.cell(f.MD5Hash),
		})
	}
	if err := output.Rows(app.Out, app.Format(), []string{"ID", "FILENAME", "SIZE", "CREATED", "MD5"}, rows, app.borders()); err != nil {
		return err
	}
	if pg.Metadata.Total > len(pg.Items) {
		app.footer("%d of %d files, newest first; narrow the search to see the rest", len(pg.Items), pg.Metadata.Total)
	} else {
		app.footer("%d files", len(pg.Items))
	}
	return nil
}

// ---- list -------------------------------------------------------------------

type FilesListCmd struct {
	Unused       bool   `help:"Only files nothing uses: candidates for cleaning up."`
	ArchivedOnly bool   `name:"archived-only" help:"Only files used by archived Smart Software versions and nothing else: candidates for cleaning up once those versions are deleted."`
	Name         string `help:"Only files whose name contains this text (case-insensitive)."`
	Sort         string `help:"Column to sort by: created (default, newest first), size (largest first), uses (most first) or name (A-Z)."`
	Desc         bool   `help:"Reverse the column's default order."`
}

// fileUses is a file with the places Addigy tracks it as used: Smart Software
// versions (downloads and uploaded icons, archived versions included), Self
// Service, policies.
type fileUses struct {
	addigy.File
	Usages []addigy.FileUsage `json:"usages"`
	// ArchivedOnly: every use is an archived Smart Software version. Set by
	// files list only.
	ArchivedOnly bool `json:"archived_only"`
}

func (c *FilesListCmd) Run(app *App) error {
	col := strings.ToLower(c.Sort)
	if col == "" {
		col = "created"
	}
	if _, ok := fileSortMostFirst[col]; !ok {
		return fmt.Errorf("unknown --sort column %q (use one of: created, size, uses, name)", col)
	}
	if c.Unused && c.ArchivedOnly {
		return errors.New("--unused and --archived-only cannot be combined: a file is one or the other")
	}
	all, err := allFileUses(app)
	if err != nil {
		return err
	}
	if err := markArchivedOnly(app, all); err != nil {
		return err
	}

	needle := strings.ToLower(c.Name)
	shown := []fileUses{}
	for _, fu := range all {
		if c.Unused && len(fu.Usages) > 0 || c.ArchivedOnly && !fu.ArchivedOnly || !strings.Contains(strings.ToLower(fu.Filename), needle) {
			continue
		}
		shown = append(shown, fu)
	}
	sortFileUses(shown, col, fileSortMostFirst[col] != c.Desc)

	if app.json() {
		return output.JSON(app.Out, shown)
	}
	style, err := app.DateStyle()
	if err != nil {
		return err
	}
	var unused, archived int
	var unusedSize, archivedSize int64
	rows := make([][]string, 0, len(shown))
	for _, f := range shown {
		if len(f.Usages) == 0 {
			unused++
			unusedSize += f.Size
		}
		if f.ArchivedOnly {
			archived++
			archivedSize += f.Size
		}
		id, name, usedBy := f.ID, f.Filename, fileUsedBy(f.Usages)
		if f.ArchivedOnly && !app.csv() {
			usedBy = "archived only: " + usedBy
		}
		var size any = f.Size
		// Files from before 2020 can have IDs of hundreds of characters;
		// CSV and JSON keep them whole.
		if !app.csv() {
			id = output.Truncate(id, 36)
			name = output.Truncate(name, 60)
			size = humanSize(f.Size)
			usedBy = output.Truncate(usedBy, 60)
		}
		rows = append(rows, []string{
			app.cell(id), app.cell(name), app.cell(size), app.cell(style.DateTime(f.Created)),
			app.cell(fmt.Sprint(len(f.Usages))), app.cell(usedBy),
		})
		if app.csv() {
			rows[len(rows)-1] = append(rows[len(rows)-1], fmt.Sprint(f.ArchivedOnly))
		}
	}
	headers := []string{"ID", "FILENAME", "SIZE", "CREATED", "USES", "USED BY"}
	if app.csv() {
		headers = append(headers, "ARCHIVED ONLY")
	}
	// With --unused the usage columns say nothing; CSV keeps them so its
	// columns never change.
	if c.Unused && !app.csv() {
		headers = headers[:4]
		for i := range rows {
			rows[i] = rows[i][:4]
		}
	}
	if err := output.Rows(app.Out, app.Format(), headers, rows, app.borders()); err != nil {
		return err
	}
	app.footer("%d files, %d unused (%s), %d used only by archived versions (%s)", len(shown), unused, humanSize(unusedSize), archived, humanSize(archivedSize))
	return nil
}

// markArchivedOnly sets ArchivedOnly on the files whose every use is an
// archived Smart Software version (a download or uploaded icon of it).
func markArchivedOnly(app *App, files []fileUses) error {
	versions, err := allSmartSoftware(app, addigy.SmartSoftwareQuery{}) // archived and active
	if err != nil {
		return err
	}
	archived := map[string]bool{} // instruction ID -> archived
	for _, v := range versions {
		archived[v.InstructionID] = v.Archived
	}
	for i, f := range files {
		files[i].ArchivedOnly = len(f.Usages) > 0
		for _, u := range f.Usages {
			if u.FeatureType != "ansible-custom-software" || !archived[u.ItemID] {
				files[i].ArchivedOnly = false
				break
			}
		}
	}
	return nil
}

// allFileUses fetches every uploaded file and where each is used.
func allFileUses(app *App) ([]fileUses, error) {
	files, err := allFiles(app, addigy.FileQuery{})
	if err != nil {
		return nil, err
	}
	return withUsages(app, files)
}

// withUsages pairs files with where Addigy tracks them as used, in one call.
func withUsages(app *App, files []addigy.File) ([]fileUses, error) {
	byID := map[string][]addigy.FileUsage{}
	if len(files) > 0 {
		api, err := app.API()
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(files))
		for i, f := range files {
			ids[i] = f.ID
		}
		usages, err := api.FileUsages(app.Ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, u := range usages {
			byID[u.FileID] = append(byID[u.FileID], u)
		}
	}
	out := make([]fileUses, len(files))
	for i, f := range files {
		out[i] = fileUses{File: f, Usages: byID[f.ID]}
		if out[i].Usages == nil {
			out[i].Usages = []addigy.FileUsage{}
		}
	}
	return out, nil
}

// fileSortMostFirst lists the --sort columns; true ones default to
// descending (newest, largest, most first).
var fileSortMostFirst = map[string]bool{"created": true, "size": true, "uses": true, "name": false}

func sortFileUses(files []fileUses, col string, desc bool) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		var c int
		switch col {
		case "created":
			c = cmpString(a.Created, b.Created) // RFC 3339 in UTC: sorts as text
		case "size":
			c = cmpInt(int(a.Size), int(b.Size))
		case "uses":
			c = cmpInt(len(a.Usages), len(b.Usages))
		case "name":
			c = cmpString(a.Filename, b.Filename)
		}
		if c == 0 {
			return cmpString(a.Filename, b.Filename) < 0 // ties A-Z either way
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
}

// fileUsedBy names what uses a file. Items Addigy has no name for (it says
// "Not available", e.g. for a policy) show their type and ID instead.
func fileUsedBy(usages []addigy.FileUsage) string {
	names := make([]string, len(usages))
	for i, u := range usages {
		names[i] = u.ItemName
		if names[i] == "" || names[i] == "Not available" {
			names[i] = u.FeatureType + " " + u.ItemID
		}
	}
	return strings.Join(names, ", ")
}

// humanSize renders a byte count in decimal units, as macOS's Finder does.
func humanSize(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, unit := range []string{"KB", "MB", "GB"} {
		v /= 1000
		if v < 1000 {
			return fmt.Sprintf("%.1f %s", v, unit)
		}
	}
	return fmt.Sprintf("%.1f TB", v/1000)
}
