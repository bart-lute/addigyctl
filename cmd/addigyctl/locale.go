package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// systemRegion returns the two-letter region of the machine's current
// locale. On macOS, System Settings lets language and regional format (date,
// number, currency notation) be set independently — e.g. an English UI with
// region set to the Netherlands shows as AppleLocale "en_US@rg=nlzzzz" — so
// that takes precedence there. Elsewhere (and as a macOS fallback), it reads
// LC_ALL, LC_TIME or LANG, in that order (matching glibc's precedence). It
// returns "" if no region can be determined, e.g. for "C" or "POSIX".
func systemRegion() string {
	if runtime.GOOS == "darwin" {
		if region := appleLocaleRegion(); region != "" {
			return region
		}
	}
	for _, key := range []string{"LC_ALL", "LC_TIME", "LANG"} {
		if region := parseLocaleRegion(os.Getenv(key)); region != "" {
			return region
		}
	}
	return ""
}

// appleLocaleCmd runs the command that reads macOS's current locale; a
// variable so tests can stub it without shelling out or depending on the
// test machine's own settings.
var appleLocaleCmd = func() ([]byte, error) {
	return exec.Command("defaults", "read", "-g", "AppleLocale").Output()
}

// appleLocaleRegion reads the region from appleLocaleCmd's output, e.g.
// "en_US" or "en_US@rg=nlzzzz" (an explicit regional-format override). It
// returns "" if the region can't be determined, such as on a fresh account
// with no locale set yet.
func appleLocaleRegion() string {
	out, err := appleLocaleCmd()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if _, rg, ok := strings.Cut(v, "@rg="); ok && len(rg) >= 2 {
		return strings.ToUpper(rg[:2])
	}
	return parseLocaleRegion(v)
}

// parseLocaleRegion extracts the two-letter region from a locale string such
// as "en_US.UTF-8" or "nl_NL". It returns "" if none is set or none can be
// parsed, e.g. "C" or "POSIX" yield "".
func parseLocaleRegion(v string) string {
	if v == "" || v == "C" || v == "POSIX" {
		return ""
	}
	v = strings.SplitN(v, ".", 2)[0] // drop encoding, e.g. ".UTF-8"
	v = strings.SplitN(v, "@", 2)[0] // drop modifier, e.g. "@euro"
	if _, region, ok := strings.Cut(v, "_"); ok && len(region) == 2 {
		return strings.ToUpper(region)
	}
	return ""
}

// mdyRegions and ymdRegions group the countries whose everyday date notation
// differs from day-month-year, the convention used elsewhere (and the
// fallback when the locale can't be determined). This is necessarily a
// simplification: it picks each country's dominant convention, not every
// exception.
var (
	mdyRegions = map[string]bool{"US": true}
	ymdRegions = map[string]bool{
		"CN": true, "JP": true, "KR": true, "TW": true,
		"HU": true, "LT": true, "IR": true,
	}
)

// defaultDatePattern returns the friendly date pattern (see
// output.TranslateDatePattern) matching the machine's current locale.
func defaultDatePattern() string {
	switch region := systemRegion(); {
	case mdyRegions[region]:
		return "mm-dd-yyyy hh:mm:ss"
	case ymdRegions[region]:
		return "yyyy-mm-dd hh:mm:ss"
	default:
		return "dd-mm-yyyy hh:mm:ss"
	}
}
