package main

import (
	"fmt"

	"github.com/ginkio/addigyctl/internal/config"
	"github.com/ginkio/addigyctl/internal/output"
)

type ConfigCmd struct {
	Path ConfigPathCmd `cmd:"" help:"Print the config file location."`
	Init ConfigInitCmd `cmd:"" help:"Create a config file template (mode 0600) if none exists."`
	Show ConfigShowCmd `cmd:"" help:"Show the effective configuration (API key redacted)."`
}

type ConfigPathCmd struct{}

func (c *ConfigPathCmd) Run(app *App) error {
	fmt.Fprintln(app.Out, app.CfgPath)
	return nil
}

type ConfigInitCmd struct{}

func (c *ConfigInitCmd) Run(app *App) error {
	if err := config.Init(app.CfgPath); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "Created %s\nAdd your API key (Addigy: Account → Integrations), or set ADDIGY_API_KEY.\n", app.CfgPath)
	return nil
}

type ConfigShowCmd struct{}

func (c *ConfigShowCmd) Run(app *App) error {
	if err := app.noCSV("config show"); err != nil {
		return err
	}
	view := struct {
		ConfigFile  string   `json:"config_file"`
		APIKey      string   `json:"api_key"`
		BaseURL     string   `json:"base_url"`
		OrgID       string   `json:"org_id"`
		DeviceFacts []string `json:"device_facts"`
	}{
		ConfigFile:  app.CfgPath,
		APIKey:      redact(app.G.APIKey),
		BaseURL:     app.G.BaseURL,
		OrgID:       output.Value(app.G.OrgID),
		DeviceFacts: firstNonEmpty(app.Cfg.DeviceFacts, defaultDeviceFacts),
	}
	if app.G.OrgID == "" {
		view.OrgID = "(discovered from policies when needed)"
	}
	return output.JSON(app.Out, view)
}

func redact(key string) string {
	switch {
	case key == "":
		return "(not set)"
	case len(key) <= 8:
		return "****"
	default:
		return "****" + key[len(key)-4:]
	}
}
