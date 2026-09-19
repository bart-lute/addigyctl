// Command addigyctl queries the Addigy API (v2) for a single tenant.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/alecthomas/kong"

	"github.com/ginkio/addigyctl/internal/output"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

// Globals are the flags shared by every command. Each can also be set with an
// environment variable or in the config file (flags > env > config file).
type Globals struct {
	APIKey     string `name:"api-key" env:"ADDIGY_API_KEY" hidden:"" help:"Addigy API key. Prefer the env var or config file over this flag."`
	BaseURL    string `name:"base-url" env:"ADDIGY_BASE_URL" help:"API base URL (default https://api.addigy.com/api/v2)."`
	OrgID      string `name:"org-id" env:"ADDIGY_ORG_ID" help:"Organization ID. Discovered from your policies when unset."`
	ConfigFile string `name:"config-file" env:"ADDIGYCTL_CONFIG" help:"Path to the config file (default: <user config dir>/addigyctl/config.json)."`
	Output     string `name:"output" short:"o" placeholder:"FORMAT" help:"Output format: table (default), json or csv."`
	JSON       bool   `name:"json" short:"j" help:"Print raw JSON instead of a table (same as --output json)."`
	Debug      bool   `name:"debug" help:"Log HTTP requests to stderr."`
}

// Format returns the requested output format. --json is shorthand for
// --output json.
func (g *Globals) Format() (output.Format, error) {
	f, err := output.ParseFormat(g.Output)
	if err != nil {
		return "", err
	}
	if g.JSON {
		if g.Output != "" && f != output.FormatJSON {
			return "", fmt.Errorf("--json cannot be combined with --output %s", g.Output)
		}
		return output.FormatJSON, nil
	}
	return f, nil
}

// CLI is the command tree.
type CLI struct {
	Globals

	Version kong.VersionFlag `name:"version" help:"Print version and exit."`

	Devices  DevicesCmd  `cmd:"" help:"Query devices."`
	Policies PoliciesCmd `cmd:"" help:"Query policies."`
	Facts    FactsCmd    `cmd:"" help:"Discover the fact identifiers available for devices."`
	Config   ConfigCmd   `cmd:"" help:"Manage the config file."`
}

func main() {
	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("addigyctl"),
		kong.Description("Query the Addigy API (v2) for a single tenant."),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Vars{"version": version},
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	app, err := newApp(ctx, &cli.Globals)
	kctx.FatalIfErrorf(err)
	kctx.FatalIfErrorf(kctx.Run(app))
}
