package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFile(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if f.APIKey != "" || f.OrgID != "" {
		t.Errorf("expected empty config, got %+v", f)
	}
}

func TestInitThenLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.BaseURL != Template.BaseURL || len(f.DeviceFacts) != 3 {
		t.Errorf("unexpected config: %+v", f)
	}
	if err := Init(path); err == nil {
		t.Error("second Init should refuse to overwrite")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"apikey":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "apikey") {
		t.Errorf("expected unknown-field error, got %v", err)
	}
}

func TestWarnIfLoose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	WarnIfLoose(path, &buf)
	if !strings.Contains(buf.String(), "warning") {
		t.Errorf("expected a warning, got %q", buf.String())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	WarnIfLoose(path, &buf)
	if buf.Len() != 0 {
		t.Errorf("unexpected warning: %q", buf.String())
	}
}
