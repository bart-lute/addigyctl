package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/config"
	"github.com/ginkio/addigyctl/internal/output"
)

// App carries resolved configuration and lazily created API access to the
// commands (it is passed to each command's Run method).
type App struct {
	Ctx     context.Context
	G       *Globals
	Cfg     config.File
	CfgPath string
	Out     io.Writer
	Err     io.Writer

	api   *addigy.API
	orgID string
	loc   *time.Location
}

// newApp merges the config file into the globals. Precedence: flags and env
// vars (already applied by Kong) > config file > defaults.
func newApp(ctx context.Context, g *Globals) (*App, error) {
	if _, err := g.Format(); err != nil {
		return nil, err
	}
	path := expandHome(g.ConfigFile)
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if cfg.APIKey != "" {
		config.WarnIfLoose(path, os.Stderr)
	}

	if g.APIKey == "" {
		g.APIKey = cfg.APIKey
	}
	if g.BaseURL == "" {
		g.BaseURL = cfg.BaseURL
	}
	if g.BaseURL == "" {
		g.BaseURL = addigy.DefaultBaseURL
	}
	if g.OrgID == "" {
		g.OrgID = cfg.OrgID
	}

	return &App{Ctx: ctx, G: g, Cfg: cfg, CfgPath: path, Out: os.Stdout, Err: os.Stderr}, nil
}

// Format is the output format. It was validated in newApp; anything invalid
// (only possible when an App is built by hand) falls back to a table.
func (a *App) Format() output.Format {
	f, err := a.G.Format()
	if err != nil {
		return output.FormatTable
	}
	return f
}

func (a *App) csv() bool  { return a.Format() == output.FormatCSV }
func (a *App) json() bool { return a.Format() == output.FormatJSON }

// borders reports whether tables should be drawn with borders: --borders /
// --no-borders when given, else the config file's "borders" key.
func (a *App) borders() bool {
	if a.G.Borders != nil {
		return *a.G.Borders
	}
	return a.Cfg.Borders
}

// footer prints a summary line below a table. CSV output stays pure data.
func (a *App) footer(format string, args ...any) {
	if a.Format() == output.FormatTable {
		fmt.Fprintf(a.Out, "\n"+format+"\n", args...)
	}
}

// cell renders a value for one table cell or CSV field: tables show "-" for
// empty values, CSV leaves the field empty.
func (a *App) cell(v any) string {
	if a.csv() {
		return output.Field(v)
	}
	return output.Value(v)
}

// noCSV is for commands that only have JSON output.
func (a *App) noCSV(cmd string) error {
	if a.csv() {
		return fmt.Errorf("%s has no CSV output; it prints JSON", cmd)
	}
	return nil
}

// API returns the API client, creating it on first use.
func (a *App) API() (*addigy.API, error) {
	if a.api != nil {
		return a.api, nil
	}
	if a.G.APIKey == "" {
		return nil, fmt.Errorf("no API key configured: set ADDIGY_API_KEY or add \"api_key\" to %s", a.CfgPath)
	}
	var dbg io.Writer
	if a.G.Debug {
		dbg = a.Err
	}
	api, err := addigy.New(addigy.Options{BaseURL: a.G.BaseURL, APIKey: a.G.APIKey, Debug: dbg})
	if err != nil {
		return nil, err
	}
	a.api = api
	return api, nil
}

// OrgID returns the organization ID: configured, or discovered from the
// first policy the API returns (every policy carries its orgid).
func (a *App) OrgID() (string, error) {
	if a.G.OrgID != "" {
		return a.G.OrgID, nil
	}
	if a.orgID != "" {
		return a.orgID, nil
	}
	api, err := a.API()
	if err != nil {
		return "", err
	}
	pols, err := api.QueryPolicies(a.Ctx, nil)
	if err != nil {
		return "", fmt.Errorf("discovering organization ID: %w", err)
	}
	for _, p := range pols {
		if p.OrgID != "" {
			a.orgID = p.OrgID
			return a.orgID, nil
		}
	}
	return "", errors.New("could not determine the organization ID; set ADDIGY_ORG_ID or \"org_id\" in the config file")
}

// Location returns the time zone used to render dates (ADE token timestamps,
// for example), from the config file's "timezone" key. Defaults to CET.
func (a *App) Location() (*time.Location, error) {
	if a.loc != nil {
		return a.loc, nil
	}
	tz := a.Cfg.Timezone
	if tz == "" {
		tz = "CET"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("invalid \"timezone\" %q in config: %w", tz, err)
	}
	a.loc = loc
	return loc, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
