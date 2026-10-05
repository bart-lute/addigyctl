package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
	"github.com/ginkio/addigyctl/internal/swfolder"
)

type SmartSoftwareNewVersionCmd struct {
	Name string `arg:"" optional:"" help:"The item's name; its folder in the software root is named after it, as export names it. Or use --item or --dir."`
	itemTarget
	Version string   `name:"to" required:"" placeholder:"VERSION" help:"The new version: the software's own version, e.g. the app's CFBundleShortVersionString. (Not --version: that prints addigyctl's own.)"`
	File    []string `name:"file" placeholder:"FILE" help:"A download for this version, instead of item.yaml's version_downloads (repeatable): an Addigy file ID, a local file (matched by content), or an exact uploaded file name. The first one's name is {{.Filename}}."`
	DryRun  bool     `name:"dry-run" help:"Show the new version without creating it."`
	Yes     bool     `help:"Create it without asking for confirmation."`
	Force   bool     `help:"Publish despite changes made in Addigy outside this folder, or a version that is not higher than the current one."`
}

func (c *SmartSoftwareNewVersionCmd) Run(app *App) error {
	if err := app.noCSV("smart-software new-version"); err != nil {
		return err
	}
	if c.Name != "" && (c.Dir != "" || c.Item != "") {
		return errors.New("give the item's name, --item or --dir, not more than one")
	}
	if c.Name == "" && c.Dir == "" && c.Item == "" {
		return errors.New("give the item's name, --item or --dir")
	}
	dir, err := c.pathOr(app, swfolder.FolderName(c.Name))
	if err != nil {
		return err
	}
	folder, err := swfolder.Read(dir)
	if err != nil {
		return err
	}
	if folder.Identifier == "" {
		return fmt.Errorf("%s has no identifier; new-version publishes a new version of an existing item (export it first)", swfolder.ItemFile)
	}

	api, err := app.API()
	if err != nil {
		return err
	}

	versions, err := allSmartSoftware(app, addigy.SmartSoftwareQuery{Identifier: folder.Identifier})
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return fmt.Errorf("no smart software with identifier %q in Addigy", folder.Identifier)
	}
	current := slices.MaxFunc(versions, func(a, b addigy.SmartSoftware) int {
		return cmpVersion(swVersion(a), swVersion(b))
	})

	// Safety checks. A version that exists is always refused; the others can
	// be overridden.
	for _, v := range versions {
		if swVersion(v) == c.Version {
			return fmt.Errorf("%s %s already exists (instruction_id %s)", current.BaseIdentifier, c.Version, v.InstructionID)
		}
	}
	var problems []string
	if cmpVersion(c.Version, swVersion(current)) <= 0 {
		problems = append(problems, fmt.Sprintf("%s is not higher than the current version %s, so it would not become the latest", c.Version, swVersion(current)))
	}
	drift, err := checkDrift(dir, &current)
	if err != nil {
		return err
	}
	if drift != "" {
		problems = append(problems, drift)
	}
	if len(problems) > 0 && !c.Force {
		return fmt.Errorf("not publishing:\n  - %s\nUse --force to publish anyway", strings.Join(problems, "\n  - "))
	}

	// The version's own downloads: --file, or else version_downloads
	// rendered for this version. Resolved after the checks above,
	// so a missing upload never hides a more basic problem.
	var files []addigy.File
	var notes []string
	if len(c.File) > 0 {
		for _, ref := range c.File {
			f, note, err := resolveFileRef(app, ref)
			if err != nil {
				return fmt.Errorf("--file %s: %w", ref, err)
			}
			files, notes = append(files, *f), appendNote(notes, note)
		}
	} else {
		names, err := folder.VersionDownloadNames(c.Version)
		if err != nil {
			return err
		}
		for _, name := range names {
			f, note, err := resolveFileName(app, name)
			if err != nil {
				return fmt.Errorf("version_downloads: %w", err)
			}
			files, notes = append(files, *f), appendNote(notes, note)
		}
	}

	// The new version: the folder rendered for it, plus its downloads.
	vars := swfolder.Vars{Version: c.Version}
	if len(files) > 0 {
		vars.Filename = files[0].Filename
	}
	next, err := folder.Render(vars)
	if err != nil {
		return err
	}
	for _, f := range files {
		next.Downloads = append(next.Downloads, swfolder.Download{ID: f.ID})
	}
	body, err := newVersionBody(next, c.Version)
	if err != nil {
		return err
	}

	// Show what changes. With -j the preview goes to stderr, keeping stdout
	// for the JSON result.
	was, err := folderFromSmartSoftware(&current)
	if err != nil {
		return err
	}
	was.Downloads = nil
	for _, d := range current.Downloads {
		was.Downloads = append(was.Downloads, swfolder.Download{ID: d.ID})
	}
	names := map[string]string{} // file id -> name, for the preview
	for _, d := range current.Downloads {
		names[d.ID] = d.Filename
	}
	for _, f := range files {
		names[f.ID] = f.Filename
	}
	preview := app.Out
	if app.json() {
		preview = app.Err
	}
	changes := printNewVersionPreview(preview, &current, c.Version, was, next, names)
	for _, n := range notes {
		fmt.Fprintf(preview, "note: %s\n", n)
	}
	for _, p := range problems {
		fmt.Fprintf(app.Err, "warning (--force): %s\n", p)
	}

	result := newVersionResult{
		DryRun:          c.DryRun,
		Dir:             dir,
		Identifier:      current.Identifier,
		FromVersion:     swVersion(current),
		FromInstruction: current.InstructionID,
		Version:         c.Version,
		Downloads:       append([]string{}, c.File...),
		Changed:         changes,
	}
	if c.DryRun {
		if app.json() {
			return output.JSON(app.Out, result)
		}
		fmt.Fprintln(app.Out, "\nDry run: nothing was created.")
		return nil
	}
	if !c.Yes {
		ok, err := app.confirm(fmt.Sprintf("Create %s %s?", current.BaseIdentifier, c.Version))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled; nothing was created")
		}
	}

	org, err := app.OrgID()
	if err != nil {
		return err
	}
	created, err := api.NewSmartSoftwareVersion(app.Ctx, org, current.InstructionID, body)
	if err != nil {
		return err
	}
	// Record the new version as what the folder matches. Checksum it as GET
	// returns it, the same way the next drift check will see it.
	stored, err := api.SmartSoftware(app.Ctx, org, created.InstructionID)
	if err != nil {
		return fmt.Errorf("created %s %s (instruction_id %s), but could not read it back to update %s: %w",
			current.BaseIdentifier, c.Version, created.InstructionID, swfolder.StateFile, err)
	}
	if err := writeStateFor(dir, stored); err != nil {
		return fmt.Errorf("created %s %s (instruction_id %s), but could not update %s: %w",
			current.BaseIdentifier, c.Version, created.InstructionID, swfolder.StateFile, err)
	}

	result.InstructionID = created.InstructionID
	if app.json() {
		return output.JSON(app.Out, result)
	}
	fmt.Fprintf(app.Out, "\nCreated %s %s (instruction_id %s).\n", current.BaseIdentifier, c.Version, created.InstructionID)
	fmt.Fprintln(app.Out, "It does nothing until it is assigned to policies.")
	return nil
}

type newVersionResult struct {
	DryRun          bool     `json:"dry_run"`
	Dir             string   `json:"dir"`
	Identifier      string   `json:"identifier"`
	FromVersion     string   `json:"from_version"`
	FromInstruction string   `json:"from_instruction_id"`
	Version         string   `json:"version"`
	InstructionID   string   `json:"instruction_id,omitempty"` // the created version; empty on a dry run
	Downloads       []string `json:"downloads"`
	Changed         []string `json:"changed"` // item.yaml field paths and script file names that differ from the current version
}

// newVersionBody is the request body for a new version: the rendered
// folder's settings and scripts plus the version. No label: it is Addigy's,
// read-only, recording the version a new one was created from.
func newVersionBody(f swfolder.Folder, version string) (json.RawMessage, error) {
	b, err := json.Marshal(f.Item)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, err
	}
	body["version"] = version
	body["installation_script"] = f.InstallScript
	body["condition"] = f.Condition
	body["remove_script"] = f.RemoveScript
	if f.Downloads == nil {
		body["downloads"] = []any{}
	}
	return json.Marshal(body)
}

// ---- files ------------------------------------------------------------------

// resolveFileRef finds the upload a --file value means: an Addigy file ID, a
// local file (the upload with the same content), or an exact file name.
func resolveFileRef(app *App, ref string) (*addigy.File, string, error) {
	api, err := app.API()
	if err != nil {
		return nil, "", err
	}
	if uuidRE.MatchString(ref) {
		f, err := api.File(app.Ctx, ref)
		return f, "", err
	}
	if st, err := os.Stat(expandHome(ref)); err == nil && st.Mode().IsRegular() {
		sum, err := md5File(expandHome(ref))
		if err != nil {
			return nil, "", err
		}
		uploads, err := allFiles(app, addigy.FileQuery{MD5Hashes: []string{sum}})
		if err != nil {
			return nil, "", err
		}
		if len(uploads) == 0 {
			return nil, "", fmt.Errorf("no upload in Addigy has this file's content (md5 %s); upload it first", sum)
		}
		return newestFile(uploads, fmt.Sprintf("the same file (md5 %s) was uploaded %d times; using the newest", sum, len(uploads)))
	}
	return resolveFileName(app, ref)
}

// resolveFileName finds the upload named exactly name. Addigy allows several
// uploads with one name: identical ones (same MD5) are harmless, but
// different ones are refused, as there's no telling which is meant.
func resolveFileName(app *App, name string) (*addigy.File, string, error) {
	// Addigy's name search is loose; keep only exact matches.
	found, err := allFiles(app, addigy.FileQuery{SearchTerm: name})
	if err != nil {
		return nil, "", err
	}
	exact := slices.DeleteFunc(found, func(f addigy.File) bool { return f.Filename != name })
	if len(exact) == 0 {
		return nil, "", fmt.Errorf("no file named %q is uploaded in Addigy; upload it first", name)
	}
	sums := map[string]bool{}
	for _, f := range exact {
		sums[f.MD5Hash] = true
	}
	if len(sums) > 1 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d different files are named %q:\n", len(exact), name)
		for _, f := range exact {
			fmt.Fprintf(&b, "    %s  uploaded %s  md5 %s\n", f.ID, f.Created, f.MD5Hash)
		}
		b.WriteString("  pick one with --file <id>, or --file <local file> to match by content")
		return nil, "", errors.New(b.String())
	}
	return newestFile(exact, fmt.Sprintf("%q was uploaded %d times with the same content; using the newest", name, len(exact)))
}

// newestFile picks the most recently uploaded file; note is returned when
// there was more than one to pick from.
func newestFile(files []addigy.File, note string) (*addigy.File, string, error) {
	f := slices.MaxFunc(files, func(a, b addigy.File) int { return strings.Compare(a.Created, b.Created) })
	if len(files) == 1 {
		note = ""
	}
	return &f, note, nil
}

// allFiles runs q across every page.
func allFiles(app *App, q addigy.FileQuery) ([]addigy.File, error) {
	api, err := app.API()
	if err != nil {
		return nil, err
	}
	q.PerPage = 100 // the endpoint's maximum
	var all []addigy.File
	for q.Page = 1; ; q.Page++ {
		pg, err := api.SearchFiles(app.Ctx, q)
		if err != nil {
			return nil, err
		}
		all = append(all, pg.Items...)
		if len(pg.Items) == 0 || q.Page >= pg.Metadata.PageCount {
			return all, nil
		}
	}
}

func md5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func appendNote(notes []string, note string) []string {
	if note == "" {
		return notes
	}
	return append(notes, note)
}

// ---- drift ------------------------------------------------------------------

// checkDrift compares the folder's state file to the item's current version
// in Addigy. It returns a description of the drift, or "" when the current
// version is still the one the folder last matched, unchanged.
func checkDrift(dir string, current *addigy.SmartSoftware) (string, error) {
	st, err := swfolder.ReadState(dir)
	if errors.Is(err, swfolder.ErrNoState) {
		return fmt.Sprintf("the folder has no %s, so changes made in Addigy can't be detected (export the item again to create it)", swfolder.StateFile), nil
	}
	if err != nil {
		return "", err
	}
	if current.InstructionID != st.InstructionID {
		return fmt.Sprintf("Addigy's current version is %s (instruction_id %s), but the folder last matched %s (%s): it was published outside this folder",
			swVersion(*current), current.InstructionID, st.Version, st.InstructionID), nil
	}
	sum, err := contentChecksum(current)
	if err != nil {
		return "", err
	}
	if sum != st.Checksum {
		return fmt.Sprintf("version %s was edited in Addigy since the folder last matched it", swVersion(*current)), nil
	}
	return "", nil
}

// contentChecksum hashes what a version consists of: the settings and
// scripts an item folder holds, its version and its downloads. Server-managed
// fields are left out, so they can't cause false drift.
func contentChecksum(s *addigy.SmartSoftware) (string, error) {
	f, err := folderFromSmartSoftware(s)
	if err != nil {
		return "", err
	}
	dl := make([]string, len(s.Downloads))
	for i, d := range s.Downloads {
		dl[i] = d.ID
	}
	b, err := json.Marshal(struct {
		Item                       swfolder.Item
		Install, Condition, Remove string
		Version                    string
		Downloads                  []string
	}{f.Item, f.InstallScript, f.Condition, f.RemoveScript, swVersion(*s), dl})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func writeStateFor(dir string, s *addigy.SmartSoftware) error {
	sum, err := contentChecksum(s)
	if err != nil {
		return err
	}
	return swfolder.WriteState(dir, swfolder.State{InstructionID: s.InstructionID, Version: swVersion(*s), Checksum: sum})
}

// ---- preview ----------------------------------------------------------------

// printNewVersionPreview shows how the new version differs from the current
// one, and returns what changed (item.yaml field paths and script names).
func printNewVersionPreview(w io.Writer, current *addigy.SmartSoftware, version string, was, next swfolder.Folder, names map[string]string) []string {
	fmt.Fprintf(w, "New version of %s: %s → %s\n", current.BaseIdentifier, swVersion(*current), version)

	fmt.Fprintln(w, "\ndownloads:")
	label := func(id string) string {
		if n := names[id]; n != "" {
			return id + "  " + n
		}
		return id
	}
	if len(was.Downloads) == 0 && len(next.Downloads) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, d := range was.Downloads {
		if !slices.Contains(next.Downloads, d) {
			fmt.Fprintf(w, "  - %s\n", label(d.ID))
		}
	}
	for _, d := range next.Downloads {
		mark := "+"
		if slices.Contains(was.Downloads, d) {
			mark = " "
		}
		fmt.Fprintf(w, "  %s %s\n", mark, label(d.ID))
	}

	var changed []string
	a, b := flatten(itemWithout(was)), flatten(itemWithout(next))
	var fields []string
	for _, k := range slices.Sorted(keysOf(a, b)) {
		if a[k] != b[k] {
			fields = append(fields, fmt.Sprintf("  %s: %s → %s", k, orNone(a[k]), orNone(b[k])))
			changed = append(changed, swfolder.ItemFile+": "+k)
		}
	}
	if len(fields) > 0 {
		fmt.Fprintf(w, "\n%s:\n%s\n", swfolder.ItemFile, strings.Join(fields, "\n"))
	}

	var same []string
	for _, sc := range []struct{ name, a, b string }{
		{swfolder.InstallFile, was.InstallScript, next.InstallScript},
		{swfolder.ConditionFile, was.Condition, next.Condition},
		{swfolder.RemoveFile, was.RemoveScript, next.RemoveScript},
	} {
		// Write ends scripts with a newline; that alone is no change.
		if d := lineDiff(withNewline(sc.a), withNewline(sc.b), 2); d != "" {
			fmt.Fprintf(w, "\n%s:\n%s", sc.name, indent(d, "  "))
			changed = append(changed, sc.name)
		} else {
			same = append(same, sc.name)
		}
	}
	if len(fields) == 0 {
		same = append([]string{swfolder.ItemFile + " settings"}, same...)
	}
	if len(same) > 0 {
		fmt.Fprintf(w, "\nunchanged: %s\n", strings.Join(same, ", "))
	}
	return changed
}

// itemWithout is f's item without downloads, which the preview shows apart.
func itemWithout(f swfolder.Folder) swfolder.Item {
	it := f.Item
	it.Downloads = nil
	return it
}

// flatten turns a value into dotted field paths and JSON-encoded leaves:
// {"a":{"b":1}} -> {"a.b": "1"}.
func flatten(v any) map[string]string {
	b, _ := json.Marshal(v)
	var x any
	json.Unmarshal(b, &x)
	out := map[string]string{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, e := range v {
				p := k
				if path != "" {
					p = path + "." + k
				}
				walk(p, e)
			}
		case []any:
			if len(v) == 0 {
				out[path] = "[]"
			}
			for i, e := range v {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		default:
			b, _ := json.Marshal(v)
			out[path] = string(b)
		}
	}
	walk("", x)
	return out
}

func keysOf(ms ...map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		seen := map[string]bool{}
		for _, m := range ms {
			for k := range m {
				if !seen[k] {
					seen[k] = true
					if !yield(k) {
						return
					}
				}
			}
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func withNewline(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

func indent(s, prefix string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		b.WriteString(prefix + line)
	}
	return b.String()
}
