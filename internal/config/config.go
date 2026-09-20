// Package config loads addigyctl's optional JSON config file.
//
// The file lives in the platform's user config directory (on macOS:
// ~/Library/Application Support/addigyctl/config.json). Environment variables
// and command-line flags take precedence over it.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	dirName  = "addigyctl"
	fileName = "config.json"
)

// File is the on-disk configuration.
type File struct {
	APIKey      string   `json:"api_key,omitempty"`
	BaseURL     string   `json:"base_url,omitempty"`
	OrgID       string   `json:"org_id,omitempty"`
	DeviceFacts []string `json:"device_facts,omitempty"`
	// Timezone is an IANA location name (or "CET"/"UTC") used to render
	// dates, such as ADE token timestamps. Defaults to the machine's local
	// time zone.
	Timezone string `json:"timezone,omitempty"`
	// DateFormat is a friendly date pattern (e.g. "dd-mm-yyyy hh:mm:ss" or
	// "yyyy-mm-dd hh:mm:ss") used to render dates. Defaults to the notation
	// of the machine's current locale.
	DateFormat string `json:"date_format,omitempty"`
	// Borders draws table output as a bordered grid instead of
	// whitespace-separated columns. Defaults to false; overridden by
	// --borders/--no-borders.
	Borders bool `json:"borders,omitempty"`
}

// Template is what `addigyctl config init` writes.
var Template = File{
	BaseURL:     "https://api.addigy.com/api/v2",
	DeviceFacts: []string{"serial_number", "device_name", "os_version"},
}

// Dir returns the directory holding addigyctl's config, using
// os.UserConfigDir (~/Library/Application Support on macOS, $XDG_CONFIG_HOME
// or ~/.config on Linux).
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, dirName), nil
}

// DefaultPath returns the default config file location.
func DefaultPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Load reads the config file at path. A missing file is not an error and
// yields an empty File. Unknown keys are rejected to catch typos.
func Load(path string) (File, error) {
	var f File
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return f, nil
}

// Init writes the template config to path (mode 0600, directory 0700). It
// refuses to overwrite an existing file.
func Init(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Template, "", "  ")
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s already exists", path)
		}
		return err
	}
	defer fh.Close()
	_, err = fh.Write(append(data, '\n'))
	return err
}

// WarnIfLoose prints a warning to w when the config file is readable by
// group or others, since it may contain an API key.
func WarnIfLoose(path string, w io.Writer) {
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	if st.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(w, "warning: %s is accessible by other users (mode %04o); run: chmod 600 %q\n",
			path, st.Mode().Perm(), path)
	}
}
