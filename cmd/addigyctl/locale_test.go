package main

import (
	"errors"
	"runtime"
	"testing"
)

// stubAppleLocale makes appleLocaleCmd fail, as if run on a non-macOS
// machine or a fresh account, so tests can exercise the env-var fallback
// regardless of the test machine's own locale settings.
func stubAppleLocale(t *testing.T, output string) {
	t.Helper()
	orig := appleLocaleCmd
	if output == "" {
		appleLocaleCmd = func() ([]byte, error) { return nil, errors.New("stub: no AppleLocale") }
	} else {
		appleLocaleCmd = func() ([]byte, error) { return []byte(output), nil }
	}
	t.Cleanup(func() { appleLocaleCmd = orig })
}

func TestAppleLocaleRegion(t *testing.T) {
	cases := map[string]string{
		"en_US":           "US",
		"en_US@rg=nlzzzz": "NL", // regional-format override, language unchanged
		"nl_NL\n":         "NL",
		"C":               "",
		"":                "",
	}
	for in, want := range cases {
		stubAppleLocale(t, in)
		if got := appleLocaleRegion(); got != want {
			t.Errorf("appleLocaleRegion() with AppleLocale=%q = %q, want %q", in, got, want)
		}
	}
}

func TestSystemRegionPrefersAppleLocale(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("AppleLocale only takes precedence on macOS")
	}
	stubAppleLocale(t, "en_US@rg=nlzzzz")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_TIME", "")
	t.Setenv("LANG", "en_US.UTF-8")
	if got := systemRegion(); got != "NL" {
		t.Errorf("macOS region override should win over LANG, got %q", got)
	}
}

func TestSystemRegionFallsBackToEnv(t *testing.T) {
	stubAppleLocale(t, "")
	cases := map[string]string{
		"en_US.UTF-8": "US",
		"nl_NL.UTF-8": "NL",
		"ja_JP":       "JP",
		"C":           "",
		"POSIX":       "",
		"":            "",
		"en":          "",
	}
	for lang, want := range cases {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_TIME", "")
		t.Setenv("LANG", lang)
		if got := systemRegion(); got != want {
			t.Errorf("systemRegion() with LANG=%q = %q, want %q", lang, got, want)
		}
	}
}

func TestSystemRegionEnvPrecedence(t *testing.T) {
	stubAppleLocale(t, "")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("LC_TIME", "ja_JP.UTF-8")
	t.Setenv("LANG", "nl_NL.UTF-8")
	if got := systemRegion(); got != "US" {
		t.Errorf("LC_ALL should win, got %q", got)
	}
}

func TestDefaultDatePattern(t *testing.T) {
	stubAppleLocale(t, "")
	cases := map[string]string{
		"en_US.UTF-8": "mm-dd-yyyy hh:mm:ss",
		"ja_JP.UTF-8": "yyyy-mm-dd hh:mm:ss",
		"nl_NL.UTF-8": "dd-mm-yyyy hh:mm:ss",
		"":            "dd-mm-yyyy hh:mm:ss",
	}
	for lang, want := range cases {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_TIME", "")
		t.Setenv("LANG", lang)
		if got := defaultDatePattern(); got != want {
			t.Errorf("defaultDatePattern() with LANG=%q = %q, want %q", lang, got, want)
		}
	}
}
