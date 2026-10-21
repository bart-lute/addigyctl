// Command addigy-fvkeys exports the FileVault recovery keys of the devices in
// an Addigy policy to an Excel file.
//
// Addigy's public API can't return recovery keys, so this uses the internal
// endpoint of Addigy's web app, authenticated with a browser session token.
// It is a stopgap: discard it once the public API supports this. See
// README.md next to this file.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"golang.org/x/term"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/config"
	"github.com/ginkio/addigyctl/internal/output"
	"github.com/ginkio/addigyctl/internal/policytree"
	"github.com/ginkio/addigyctl/internal/xlsx"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

const (
	// defaultAppURL is Addigy's web app, which serves the keys endpoint.
	defaultAppURL = "https://app.addigy.com"

	// keysPath is the web app's (internal, undocumented) keys endpoint.
	keysPath = "/api/mdm/devices/filevault/keys"

	// sessionCookie is the one cookie the keys endpoint needs.
	sessionCookie = "prod_auth_token"

	// tokenEnv holds the session token, so it needn't be typed in.
	tokenEnv = "ADDIGY_SESSION_TOKEN"

	sheet = "FileVault keys"

	// fetchPageSize is the page size used when fetching the devices.
	fetchPageSize = 50
)

// deviceFacts are the facts fetched for each device.
var deviceFacts = []string{"device_name", "serial_number", "udid", addigy.LocationFact}

// errSession means Addigy didn't accept the session token.
var errSession = errors.New("Addigy didn't accept the session token: it has expired or is wrong. Copy a fresh prod_auth_token (see README.md) and try again")

// CLI is the command line.
type CLI struct {
	File   string `arg:"" optional:"" type:"path" help:"Excel file to write. Default: fvkeys-<policy>-<date>.xlsx in the export folder (--export-dir)."`
	Policy string `required:"" help:"Export the devices located in this policy or any of its sub-policies. Accepts an ID, a name, or a 'Parent / Child' path."`
	Direct bool   `help:"Only devices located directly in the policy, not in its sub-policies."`
	Force  bool   `help:"Overwrite the file if it already exists."`

	APIKey     string `name:"api-key" env:"ADDIGY_API_KEY" hidden:"" help:"Addigy API key. Prefer the env var or addigyctl's config file over this flag."`
	BaseURL    string `name:"base-url" env:"ADDIGY_BASE_URL" help:"API base URL (default https://api.addigy.com/api/v2)."`
	ExportDir  string `name:"export-dir" env:"ADDIGYCTL_EXPORT_DIR" placeholder:"DIR" help:"Folder for files written without a file name (default: \"export_dir\" in addigyctl's config file, else ~/addigy-exports)."`
	ConfigFile string `name:"config-file" env:"ADDIGYCTL_CONFIG" help:"Path to addigyctl's config file (default: <user config dir>/addigyctl/config.json)."`
	AppURL     string `name:"app-url" hidden:"" default:"${app_url}" help:"Addigy web app URL (for tests)."`

	Version kong.VersionFlag `name:"version" help:"Print version and exit."`
}

func main() {
	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("addigy-fvkeys"),
		kong.Description("Export the FileVault recovery keys of the devices in an Addigy policy to an Excel file. "+
			"Uses Addigy's internal web app API with your browser session token ("+tokenEnv+", else asked for); see README.md."),
		kong.UsageOnError(),
		kong.Vars{"version": version, "app_url": defaultAppURL},
	)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	kctx.FatalIfErrorf(run(ctx, &cli, env{
		out: os.Stdout, err: os.Stderr,
		token: func() (string, error) { return readToken(os.Stdin, os.Stderr) },
	}))
}

// env is what run needs from the outside world, so tests can replace it.
type env struct {
	out, err io.Writer
	token    func() (string, error) // the session token
	http     *http.Client           // optional
	now      func() time.Time       // optional
}

func run(ctx context.Context, cli *CLI, e env) error {
	cfgPath := cli.ConfigFile
	if cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		cfgPath = p
	}
	cfg, err := config.Load(config.ExpandHome(cfgPath))
	if err != nil {
		return err
	}
	apiKey := cli.APIKey
	if apiKey == "" {
		apiKey = cfg.APIKey
	}
	if apiKey == "" {
		return fmt.Errorf("no API key configured: set ADDIGY_API_KEY or add \"api_key\" to %s", cfgPath)
	}
	if cfg.APIKey != "" {
		config.WarnIfLoose(cfgPath, e.err)
	}
	baseURL := cli.BaseURL
	if baseURL == "" {
		baseURL = cfg.BaseURL
	}
	now := time.Now
	if e.now != nil {
		now = e.now
	}
	loc := time.Local
	if cfg.Timezone != "" {
		if loc, err = time.LoadLocation(cfg.Timezone); err != nil {
			return fmt.Errorf("invalid \"timezone\" %q in config: %w", cfg.Timezone, err)
		}
	}

	api, err := addigy.New(addigy.Options{BaseURL: baseURL, APIKey: apiKey})
	if err != nil {
		return err
	}
	pols, err := api.QueryPolicies(ctx, nil)
	if err != nil {
		return err
	}
	idx := policytree.New(pols)
	root, err := idx.Resolve(cli.Policy)
	if err != nil {
		return err
	}

	path := cli.File
	if path == "" {
		dir := cfg.ResolveExportDir(cli.ExportDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating the export folder: %w", err)
		}
		path = filepath.Join(dir, xlsx.FileName("fvkeys", root.Name, now()))
	}
	if !cli.Force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
		}
	}

	devices, reported, err := api.AllDevices(ctx, addigy.DeviceQuery{
		Facts:     deviceFacts,
		SortField: addigy.StableSortField,
		PerPage:   fetchPageSize,
	})
	if err != nil {
		return err
	}
	if reported > 0 && len(devices) != reported {
		fmt.Fprintf(e.err, "warning: Addigy reported %d devices but %d were received; the export may be incomplete\n", reported, len(devices))
	}
	devices = addigy.LocatedIn(devices, idx.Members(root.ID, cli.Direct))
	sortDevices(devices)
	if len(devices) == 0 {
		return fmt.Errorf("no devices in %q", idx.Path(root))
	}

	token, err := tokenFromEnv()
	if err != nil {
		return err
	}
	if token == "" {
		if token, err = e.token(); err != nil {
			return err
		}
	}
	keys := &keysClient{http: e.http, base: strings.TrimRight(cli.AppURL, "/"), token: token}

	var rows [][]any
	var nKeys, nWithout, nFailed int
	for i, d := range devices {
		fmt.Fprintf(e.err, "\rfetching keys %d/%d", i+1, len(devices))
		name, serial, udid := text(d, "device_name"), text(d, "serial_number"), text(d, "udid")
		policy := text(d, addigy.LocationFact)
		if p, ok := idx.Get(policy); ok {
			policy = idx.Path(p)
		}
		row := func(key string, created any) []any { return []any{name, serial, policy, created, key} }

		if udid == "" {
			fmt.Fprintf(e.err, "\rwarning: %s (%s) has no UDID; no keys fetched\n", name, serial)
			nFailed++
			rows = append(rows, row("", nil))
			continue
		}
		ks, err := keys.fetch(ctx, udid)
		if errors.Is(err, errSession) || ctx.Err() != nil {
			fmt.Fprintln(e.err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err != nil {
			fmt.Fprintf(e.err, "\rwarning: %s (%s): %v\n", name, serial, err)
			nFailed++
			rows = append(rows, row("", nil))
			continue
		}
		if len(ks) == 0 {
			nWithout++
			rows = append(rows, row("", nil))
			continue
		}
		for _, k := range ks {
			rows = append(rows, row(k.Key, k.createdIn(loc)))
		}
		nKeys += len(ks)
	}
	fmt.Fprintln(e.err)

	if err := xlsx.Write(path, []string{"Device Name", "Serial Number", "Policy", "Created", "Key"}, rows,
		xlsx.Options{Sheet: sheet, DateFormat: cfg.DateFormat}); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Exported %s for %s (%d without a key, %d failed) to %s\n",
		plural(nKeys, "key", "keys"), plural(len(devices), "device", "devices"), nWithout, nFailed, path)
	return nil
}

// tokenFromEnv returns the session token from tokenEnv, if set.
func tokenFromEnv() (string, error) {
	return cleanToken(os.Getenv(tokenEnv))
}

// readToken asks for the session token without echoing it.
func readToken(in *os.File, w io.Writer) (string, error) {
	if !term.IsTerminal(int(in.Fd())) {
		// Piped in: read one line.
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("no session token: set %s or run addigy-fvkeys in a terminal", tokenEnv)
		}
		return cleanToken(line)
	}
	fmt.Fprintf(w, "Addigy session token (%s cookie; input is hidden): ", sessionCookie)
	b, err := term.ReadPassword(int(in.Fd()))
	fmt.Fprintln(w)
	if err != nil {
		return "", err
	}
	return cleanToken(string(b))
}

// cleanToken trims a pasted token and accepts "prod_auth_token=<value>" too.
// A token that can't be a cookie value is refused, so it can't smuggle in
// another header.
func cleanToken(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, sessionCookie+"=")
	s = strings.TrimSuffix(s, ";")
	if strings.ContainsAny(s, " \t\r\n;,\"") {
		return "", errors.New("that doesn't look like a session token: paste only the value of the prod_auth_token cookie")
	}
	return s, nil
}

// fvKey is one recovery key as the keys endpoint returns it.
type fvKey struct {
	Key     string  `json:"key"`
	Created float64 `json:"created"` // Unix time
}

// createdIn returns when the key was created, in loc, or nil when unknown.
// created is Unix time in seconds; a value in milliseconds is recognized too.
func (k fvKey) createdIn(loc *time.Location) any {
	switch {
	case k.Created <= 0:
		return nil
	case k.Created > 1e12:
		return time.UnixMilli(int64(k.Created)).In(loc)
	}
	return time.Unix(int64(k.Created), 0).In(loc)
}

// keysClient fetches recovery keys from the web app's keys endpoint.
type keysClient struct {
	http  *http.Client
	base  string
	token string
}

// fetch returns a device's keys, newest first. A rejected session token is
// errSession.
func (c *keysClient) fetch(ctx context.Context, udid string) ([]fvKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+keysPath+"?device_udid="+url.QueryEscape(udid), nil)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.token})
	req.Header.Set("Accept", "application/json")

	hc := c.http
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	// A redirect is most likely to the login page: don't follow it.
	client := *hc
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden,
		resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, errSession
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("Addigy returned %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	var keys []fvKey
	if err := json.Unmarshal(body, &keys); err != nil {
		// Never include the body: it may hold a key.
		return nil, errors.New("Addigy's response was not the expected list of keys")
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].Created > keys[j].Created })
	return keys, nil
}

// text returns a device's fact as text ("" when missing).
func text(d addigy.Device, id string) string {
	v, _ := d.Value(id)
	return output.Field(v)
}

// sortDevices orders devices by name, then serial number.
func sortDevices(ds []addigy.Device) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := strings.ToLower(text(ds[i], "device_name")), strings.ToLower(text(ds[j], "device_name"))
		if a != b {
			return a < b
		}
		return text(ds[i], "serial_number") < text(ds[j], "serial_number")
	})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
