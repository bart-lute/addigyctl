package swfolder

import (
	"reflect"
	"strings"
	"testing"
)

func templated() Folder {
	f := sample()
	f.InstallScript = "VERSION={{.Version}}\ninstaller -pkg \"/Library/Addigy/ansible/packages/Example App ({{.Version}})/{{.Filename}}\"\n"
	f.PredefinedConditions = map[string]any{
		"app_exists": map[string]any{"enabled": true, "operator": "lt", "version": "{{.Version}}"},
	}
	return f
}

func TestRender(t *testing.T) {
	in := templated()
	out, err := in.Render(Vars{Version: "4.16.0", Filename: "Example-4.16.0.pkg"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "VERSION=4.16.0\ninstaller -pkg \"/Library/Addigy/ansible/packages/Example App (4.16.0)/Example-4.16.0.pkg\"\n"; out.InstallScript != want {
		t.Errorf("install script = %q", out.InstallScript)
	}
	if v := out.PredefinedConditions["app_exists"].(map[string]any)["version"]; v != "4.16.0" {
		t.Errorf("app_exists.version = %v", v)
	}
	// Render must not change the folder it was called on.
	if v := in.PredefinedConditions["app_exists"].(map[string]any)["version"]; v != "{{.Version}}" {
		t.Errorf("Render modified its input: app_exists.version = %v", v)
	}
}

func TestRenderErrors(t *testing.T) {
	for name, c := range map[string]struct {
		f    func() Folder
		v    Vars
		want []string
	}{
		"unknown placeholder": {
			f:    func() Folder { f := sample(); f.Condition = "{{.Versoin}}"; return f },
			v:    Vars{Version: "1.0"},
			want: []string{"condition.sh", "Versoin", "known placeholders"},
		},
		"placeholder without a value": {
			f:    templated,
			v:    Vars{Version: "1.0"}, // no Filename
			want: []string{"install.sh", "Filename"},
		},
		"bad syntax in item.yaml": {
			f:    func() Folder { f := sample(); f.Description = "{{.Version"; return f },
			v:    Vars{Version: "1.0"},
			want: []string{"item.yaml: description"},
		},
		"nested field path": {
			f: func() Folder {
				f := sample()
				f.PredefinedConditions = map[string]any{"app_exists": map[string]any{"version": "{{.Nope}}"}}
				return f
			},
			v:    Vars{Version: "1.0"},
			want: []string{"item.yaml: predefined_conditions.app_exists.version"},
		},
	} {
		_, err := c.f().Render(c.v)
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q does not mention %q", name, err, w)
			}
		}
	}
}

func TestLiteralRendersBackToItself(t *testing.T) {
	f := sample()
	f.InstallScript = "echo '{{ not a placeholder }}' {{{{\n"
	f.Description = "uses {{mustache}}"
	out, err := f.Literal().Render(Vars{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, f) {
		t.Errorf("Literal then Render changed the folder:\n in: %#v\nout: %#v", f, out)
	}
}

func TestWithPlaceholders(t *testing.T) {
	f := sample()
	f.InstallScript = "VERSION=4.15.0\ncp \"/packages/Example App (4.15.0)/Example-4.15.0.pkg\" .\n# not 14.15.0, 4.15.01 or 4.15.0.1\n"
	f.PredefinedConditions = map[string]any{"app_exists": map[string]any{"version": "4.15.0", "enabled": true}}
	f.VersionDownloads = []string{"Example-4.15.0.pkg"}
	out, counts := f.WithPlaceholders("4.15.0")
	if out.VersionDownloads[0] != "Example-{{.Version}}.pkg" {
		t.Errorf("version_downloads = %v", out.VersionDownloads)
	}

	want := "VERSION={{.Version}}\ncp \"/packages/Example App ({{.Version}})/Example-{{.Version}}.pkg\" .\n# not 14.15.0, 4.15.01 or 4.15.0.1\n"
	if out.InstallScript != want {
		t.Errorf("install script =\n%s\nwant\n%s", out.InstallScript, want)
	}
	if !reflect.DeepEqual(counts, map[string]int{"install.sh": 3, "item.yaml": 2}) {
		t.Errorf("counts = %v", counts)
	}
	// And it round-trips: rendering with the same version gives the original.
	back, err := out.Render(Vars{Version: "4.15.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, f) {
		t.Errorf("render after WithPlaceholders differs from the original")
	}
}

func TestReadHintsAtUnquotedPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ItemFile, "base_identifier: x\ndescription: {{.Version}}\n")
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "quote values") {
		t.Errorf("expected a quoting hint, got %v", err)
	}
}

func TestTemplatedFolderSurvivesWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	in := templated()
	if err := Write(dir, in, "", false); err != nil {
		t.Fatal(err)
	}
	out, err := Read(dir)
	if err != nil {
		t.Fatalf("a folder Write produced must read back: %v", err)
	}
	in.Condition += "\n" // Write ends scripts with a newline
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip changed the folder:\n in: %#v\nout: %#v", in, out)
	}
}

func TestVersionDownloadNames(t *testing.T) {
	f := sample()
	f.VersionDownloads = []string{"Example-{{.Version}}.pkg", "Example-{{.Version}}-arm64.zip"}
	names, err := f.VersionDownloadNames("2.0")
	if err != nil || !reflect.DeepEqual(names, []string{"Example-2.0.pkg", "Example-2.0-arm64.zip"}) {
		t.Errorf("names = %v, %v", names, err)
	}
	f.VersionDownloads = nil
	if names, err := f.VersionDownloadNames("2.0"); err != nil || len(names) != 0 {
		t.Errorf("no patterns: %v, %v", names, err)
	}
	// {{.Filename}} comes from these files, so a pattern can't use it.
	f.VersionDownloads = []string{"{{.Filename}}"}
	if _, err := f.VersionDownloadNames("2.0"); err == nil || !strings.Contains(err.Error(), "version_downloads[0]") {
		t.Errorf("expected an error naming the pattern, got %v", err)
	}
}

func TestUnfilledPlaceholders(t *testing.T) {
	f := sample()
	if got := f.UnfilledPlaceholders(); len(got) != 1 || !strings.HasPrefix(got[0], "item.yaml: version_downloads[0]") {
		t.Errorf("sample's version_downloads pattern: %v", got)
	}
	f.VersionDownloads = nil
	f.InstallScript = `cp "/x/App ({{.Version}})/app.pkg" /tmp; echo "${HOME}" '{{ not one }}'`
	f.PredefinedConditions = map[string]any{"app_exists": map[string]any{"version": "{{ .Filename }}"}}
	got := f.UnfilledPlaceholders()
	want := []string{"install.sh ({{.Version}})", "item.yaml: predefined_conditions.app_exists.version ({{ .Filename }})"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	f.InstallScript, f.PredefinedConditions = "echo {{literal}} ${VERSION}", nil
	if got := f.UnfilledPlaceholders(); len(got) != 0 {
		t.Errorf("false positives: %q", got)
	}
}
