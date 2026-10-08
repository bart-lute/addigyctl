// Package swfolder reads and writes addigyctl's on-disk format for one Smart
// Software item: a folder holding item.yaml (every editable setting that is
// not a script) and one file per script.
//
// The folder may hold other files too (a software repo's build or test
// configuration, say); this package only ever touches the files it owns,
// listed by OwnedFiles.
package swfolder

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// File names inside an item folder.
const (
	ItemFile      = "item.yaml"
	InstallFile   = "install.sh"
	ConditionFile = "condition.sh"
	RemoveFile    = "remove.sh"
)

// OwnedFiles are the files in an item folder that belong to addigyctl.
func OwnedFiles() []string {
	return []string{ItemFile, InstallFile, ConditionFile, RemoveFile, StateFile}
}

// Item is item.yaml: the editable, version-independent settings of a Smart
// Software item, named as in Addigy's API so the json tags double as the
// request body. Server-managed fields (label, provider, organization_id, ...)
// and per-version values (version, the version's installer) are not part of
// it.
type Item struct {
	// Identifier is shared by every version of the item; set by export.
	Identifier           string         `json:"identifier,omitempty" yaml:"identifier"`
	BaseIdentifier       string         `json:"base_identifier" yaml:"base_identifier"`
	Category             string         `json:"category" yaml:"category"`
	Description          string         `json:"description" yaml:"description"`
	Priority             float64        `json:"priority" yaml:"priority"`
	RunOnSuccess         bool           `json:"run_on_success" yaml:"run_on_success"`
	StatusOnSkipped      string         `json:"status_on_skipped" yaml:"status_on_skipped"`
	PredefinedConditions map[string]any `json:"predefined_conditions" yaml:"predefined_conditions"`
	Profiles             []any          `json:"profiles" yaml:"profiles"`
	SoftwareIcon         *Icon          `json:"software_icon,omitempty" yaml:"software_icon,omitempty"`
	// Downloads are files every version needs, by file ID.
	Downloads []Download `json:"downloads" yaml:"downloads"`
	// VersionDownloads name the files each version needs, as patterns such
	// as "helloworld-{{.Version}}": publishing uses the upload whose name is
	// exactly the rendered pattern. Edit the list when a release needs more,
	// fewer or other files. addigyctl's own; never sent to Addigy.
	VersionDownloads []string `json:"-" yaml:"version_downloads"`
}

// Icon is the item's icon: an uploaded file (provider "cloud-storage", ID a
// file ID) or an image URL (provider "web", ID the URL).
type Icon struct {
	ID       string `json:"id" yaml:"id"`
	Provider string `json:"provider" yaml:"provider"`
}

// Download references an uploaded file by its Addigy file ID.
type Download struct {
	ID string `json:"id" yaml:"id"`
}

// Folder is a whole item folder: item.yaml plus the scripts. An empty script
// has no file.
type Folder struct {
	Item
	InstallScript string
	Condition     string
	RemoveScript  string
}

func (f Folder) scripts() map[string]string {
	return map[string]string{
		InstallFile:   f.InstallScript,
		ConditionFile: f.Condition,
		RemoveFile:    f.RemoveScript,
	}
}

// Write writes f to dir, creating dir if needed. header, if not empty, is
// written as a comment at the top of item.yaml. Unless force is set, Write
// refuses when dir already holds any of the files addigyctl owns; with force,
// it replaces them and removes script files f no longer has. Even with force,
// it refuses when dir holds a different item (another identifier), so two
// items can never overwrite each other's folder. Other files in dir are left
// alone.
func Write(dir string, f Folder, header string, force bool) error {
	if other := existingIdentifier(dir); other != "" && f.Identifier != "" && other != f.Identifier {
		return fmt.Errorf("%s holds a different item (%s), not %s", dir, other, f.Identifier)
	}
	if !force {
		var existing []string
		for _, name := range OwnedFiles() {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				existing = append(existing, name)
			}
		}
		if len(existing) > 0 {
			return fmt.Errorf("%s already holds %s (use --force to overwrite)", dir, strings.Join(existing, ", "))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var buf bytes.Buffer
	for line := range strings.Lines(header) {
		buf.WriteString(strings.TrimRight("# "+strings.TrimSuffix(line, "\n"), " ") + "\n")
	}
	if header != "" {
		buf.WriteString("\n")
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f.Item); err != nil {
		return fmt.Errorf("encoding %s: %w", ItemFile, err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ItemFile), buf.Bytes(), 0o644); err != nil {
		return err
	}

	for name, body := range f.scripts() {
		path := filepath.Join(dir, name)
		if body == "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		// End with a newline, as editors and Git expect.
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// existingIdentifier is the identifier in dir's item.yaml, or "" when there
// is none or it can't be read.
func existingIdentifier(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ItemFile))
	if err != nil {
		return ""
	}
	var it struct {
		Identifier string `yaml:"identifier"`
	}
	if yaml.Unmarshal(data, &it) != nil {
		return ""
	}
	return it.Identifier
}

// FolderName turns an item's name into a folder name: lowercase, with every
// run of characters other than letters, digits, "." and "_" replaced by one
// "-" ("Acme/User Config" -> "acme-user-config"). It is "" when nothing
// usable is left.
func FolderName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return strings.Trim(b.String(), ".")
}

// Read reads the item folder dir. A missing script file is an empty script;
// a missing item.yaml is an error. Unknown keys in item.yaml are rejected to
// catch typos.
func Read(dir string) (Folder, error) {
	var f Folder
	data, err := os.ReadFile(filepath.Join(dir, ItemFile))
	if err != nil {
		return f, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f.Item); err != nil {
		err = fmt.Errorf("%s: %w", filepath.Join(dir, ItemFile), err)
		if bytes.Contains(data, []byte("{{")) {
			// version: {{.Version}} parses as a YAML flow mapping.
			err = fmt.Errorf("%w (quote values with placeholders: \"{{.Version}}\")", err)
		}
		return f, err
	}

	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return string(b), err
	}
	if f.InstallScript, err = read(InstallFile); err != nil {
		return f, err
	}
	if f.Condition, err = read(ConditionFile); err != nil {
		return f, err
	}
	if f.RemoveScript, err = read(RemoveFile); err != nil {
		return f, err
	}
	return f, nil
}
