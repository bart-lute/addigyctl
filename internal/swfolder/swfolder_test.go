package swfolder

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sample() Folder {
	return Folder{
		Item: Item{
			Identifier:      "Example App-1234",
			BaseIdentifier:  "Example App",
			Category:        "Utilities",
			Priority:        5,
			RunOnSuccess:    true,
			StatusOnSkipped: "finished",
			PredefinedConditions: map[string]any{
				"app_exists": map[string]any{"enabled": true, "path": "/Applications/Example.app"},
			},
			Profiles:         []any{},
			SoftwareIcon:     &Icon{ID: "https://example.com/icon.png", Provider: "web"},
			Downloads:        []Download{{ID: "f1"}},
			VersionDownloads: []string{"Example-{{.Version}}.pkg"},
		},
		InstallScript: "#!/bin/sh\necho install\n",
		Condition:     "exit 0", // no trailing newline: Write adds one
	}
}

func TestWriteThenRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "example")
	in := sample()
	if err := Write(dir, in, "Exported from version 1.0.\nSecond line.", false); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ItemFile))
	if !strings.HasPrefix(string(data), "# Exported from version 1.0.\n# Second line.\n\nidentifier: Example App-1234\n") {
		t.Errorf("unexpected item.yaml:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, RemoveFile)); err == nil {
		t.Error("an empty remove script should have no file")
	}

	out, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	in.Condition += "\n"
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip changed the folder:\n in: %#v\nout: %#v", in, out)
	}
}

func TestWriteRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "build.yaml")
	if err := os.WriteFile(other, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Files addigyctl doesn't own are no reason to refuse.
	if err := Write(dir, sample(), "", false); err != nil {
		t.Fatal(err)
	}
	err := Write(dir, sample(), "", false)
	if err == nil || !strings.Contains(err.Error(), "item.yaml") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected a refusal naming item.yaml, got %v", err)
	}

	// With force, scripts the item no longer has are removed; other files stay.
	f := sample()
	f.Condition = ""
	if err := Write(dir, f, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ConditionFile)); err == nil {
		t.Error("stale condition.sh was not removed")
	}
	if b, _ := os.ReadFile(other); string(b) != "keep me" {
		t.Error("a file addigyctl doesn't own was changed")
	}
}

func TestReadRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ItemFile), []byte("base_identifier: x\npriorty: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "priorty") {
		t.Errorf("expected an unknown-key error, got %v", err)
	}
}

func TestFolderName(t *testing.T) {
	for in, want := range map[string]string{
		"Adobe Remote Update Manager":   "adobe-remote-update-manager",
		"Akvo/user-configurator-config": "akvo-user-configurator-config",
		"Backblaze Installer.app":       "backblaze-installer.app",
		"  Zoom (Rooms) ":               "zoom-rooms",
		"..":                            "",
		"///":                           "",
	} {
		if got := FolderName(in); got != want {
			t.Errorf("FolderName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteRefusesADifferentItemEvenWithForce(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, sample(), "", false); err != nil {
		t.Fatal(err)
	}
	other := sample()
	other.Identifier = "Other App-5678"
	err := Write(dir, other, "", true)
	if err == nil || !strings.Contains(err.Error(), "Example App-1234") {
		t.Errorf("expected a refusal naming the item already there, got %v", err)
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestState(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadState(dir); err != ErrNoState {
		t.Errorf("missing state: got %v, want ErrNoState", err)
	}
	in := State{InstructionID: "i1", Version: "1.0", Checksum: "sha256:abc"}
	if err := WriteState(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadState(dir)
	if err != nil || out != in {
		t.Errorf("round trip: %+v, %v", out, err)
	}
	writeFile(t, dir, StateFile, "instruction_id: i1\n")
	if _, err := ReadState(dir); err == nil {
		t.Error("a state without a checksum should be an error")
	}
}
