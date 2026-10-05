package swfolder

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"
)

// Vars are the values placeholders in an item folder can use: {{.Version}}
// and {{.Filename}}. A placeholder whose value is empty is an error, like an
// unknown one, so a forgotten value never silently renders as "".
type Vars struct {
	Version  string // the version being published
	Filename string // the file name of the version's (first) download
}

func (v Vars) data() map[string]string {
	m := map[string]string{}
	if v.Version != "" {
		m["Version"] = v.Version
	}
	if v.Filename != "" {
		m["Filename"] = v.Filename
	}
	return m
}

// Render fills in the placeholders (Go text/template syntax) in the scripts
// and in every text value of item.yaml. It returns a new Folder; f is not
// changed.
func (f Folder) Render(v Vars) (Folder, error) {
	data := v.data()
	return f.mapStrings(func(where, s string) (string, error) {
		if !strings.Contains(s, "{{") {
			return s, nil
		}
		t, err := template.New(where).Option("missingkey=error").Parse(s)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return "", fmt.Errorf("%w (known placeholders: {{.Version}}, {{.Filename}})", err)
		}
		return b.String(), nil
	})
}

// VersionDownloadNames renders the version_downloads patterns for version:
// the exact names of the files that version needs. Patterns can only use
// {{.Version}}; {{.Filename}} is derived from these files.
func (f Folder) VersionDownloadNames(version string) ([]string, error) {
	g := Folder{Item: Item{VersionDownloads: f.VersionDownloads}}
	r, err := g.Render(Vars{Version: version})
	if err != nil {
		return nil, err
	}
	return r.VersionDownloads, nil
}

// Literal escapes every "{{" in f, so that rendering it gives back f exactly.
// Export uses it: content from Addigy is literal text, not a template.
func (f Folder) Literal() Folder {
	out, _ := f.mapStrings(func(_, s string) (string, error) {
		return strings.ReplaceAll(s, "{{", `{{"{{"}}`), nil
	})
	return out
}

// WithPlaceholders replaces each occurrence of version in f with
// {{.Version}}, and reports how many it replaced per file (item.yaml or a
// script's file name). An occurrence must stand on its own: "4.1" is not
// replaced inside "4.15" or "14.1". It is plain text matching, so the result
// needs review: a version like "1.0" also matches unrelated text such as
// <?xml version="1.0"?>.
func (f Folder) WithPlaceholders(version string) (Folder, map[string]int) {
	counts := map[string]int{}
	out, _ := f.mapStrings(func(where, s string) (string, error) {
		r, n := replaceVersion(s, version)
		if n > 0 {
			counts[strings.SplitN(where, ":", 2)[0]] += n
		}
		return r, nil
	})
	return out, counts
}

func replaceVersion(s, version string) (string, int) {
	if version == "" {
		return s, 0
	}
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	var b strings.Builder
	n, i := 0, 0
	for i < len(s) {
		j := strings.Index(s[i:], version)
		if j < 0 {
			break
		}
		start, end := i+j, i+j+len(version)
		// Part of a longer version: a digit right before or after, or a
		// "." joining it to one ("4.15" for "4.1", "14.1" for "4.1").
		before := start > 0 && (isDigit(s[start-1]) || s[start-1] == '.' && start > 1 && isDigit(s[start-2]))
		after := end < len(s) && (isDigit(s[end]) || s[end] == '.' && end+1 < len(s) && isDigit(s[end+1]))
		b.WriteString(s[i:start])
		if before || after {
			b.WriteString(version)
		} else {
			b.WriteString("{{.Version}}")
			n++
		}
		i = end
	}
	if n == 0 {
		return s, 0
	}
	b.WriteString(s[i:])
	return b.String(), n
}

// mapStrings returns a deep copy of f with fn applied to every script and
// every string in the item, in a fixed order. where names the string for
// error messages: a script's file name, or "item.yaml: <field path>".
func (f Folder) mapStrings(fn func(where, s string) (string, error)) (Folder, error) {
	var err error
	str := func(where, s string) string {
		if err != nil {
			return s
		}
		var r string
		if r, err = fn(where, s); err != nil {
			err = fmt.Errorf("%s: %w", where, err)
		}
		return r
	}
	field := func(path string) string { return ItemFile + ": " + path }

	out := f
	out.InstallScript = str(InstallFile, f.InstallScript)
	out.Condition = str(ConditionFile, f.Condition)
	out.RemoveScript = str(RemoveFile, f.RemoveScript)

	out.Identifier = str(field("identifier"), f.Identifier)
	out.BaseIdentifier = str(field("base_identifier"), f.BaseIdentifier)
	out.Category = str(field("category"), f.Category)
	out.Description = str(field("description"), f.Description)
	out.StatusOnSkipped = str(field("status_on_skipped"), f.StatusOnSkipped)
	if f.PredefinedConditions != nil {
		out.PredefinedConditions = mapAny("predefined_conditions", f.PredefinedConditions, func(p, s string) string { return str(field(p), s) }).(map[string]any)
	}
	if f.Profiles != nil {
		out.Profiles = mapAny("profiles", f.Profiles, func(p, s string) string { return str(field(p), s) }).([]any)
	}
	if f.SoftwareIcon != nil {
		icon := *f.SoftwareIcon
		icon.ID = str(field("software_icon.id"), icon.ID)
		icon.Provider = str(field("software_icon.provider"), icon.Provider)
		out.SoftwareIcon = &icon
	}
	if f.VersionDownloads != nil {
		out.VersionDownloads = make([]string, len(f.VersionDownloads))
		for i, p := range f.VersionDownloads {
			out.VersionDownloads[i] = str(field(fmt.Sprintf("version_downloads[%d]", i)), p)
		}
	}
	if f.Downloads != nil {
		out.Downloads = make([]Download, len(f.Downloads))
		for i, d := range f.Downloads {
			out.Downloads[i] = Download{ID: str(field(fmt.Sprintf("downloads[%d].id", i)), d.ID)}
		}
	}
	return out, err
}

// mapAny deep-copies a decoded YAML/JSON value, applying fn to its strings.
// Map keys are visited in sorted order so errors are deterministic.
func mapAny(path string, v any, fn func(path, s string) string) any {
	switch v := v.(type) {
	case string:
		return fn(path, v)
	case map[string]any:
		m := make(map[string]any, len(v))
		for _, k := range slices.Sorted(maps.Keys(v)) {
			m[k] = mapAny(path+"."+k, v[k], fn)
		}
		return m
	case []any:
		s := make([]any, len(v))
		for i, e := range v {
			s[i] = mapAny(fmt.Sprintf("%s[%d]", path, i), e, fn)
		}
		return s
	default:
		return v
	}
}
