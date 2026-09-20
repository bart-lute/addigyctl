# addigyctl

A small command line tool to query the [Addigy](https://addigy.com) v2 API for a single tenant. It is read-only and covers devices, policies, facts, ADE tokens and alerts.

The API client is not written by hand: it is generated from the Swagger spec Addigy publishes, so the data types always match the API.

```
$ addigyctl policies tree
Acme [5]
├── Finance [3]
│   └── Laptops [2]
└── Sales [1]
Other Root [1]

5 policies
[n] = devices in the policy and all of its sub-policies.
```

## Requirements

- Go 1.24 or newer
- An Addigy API key (Addigy: Account → Integrations)

## Build

```sh
make setup      # one-time: pins oapi-codegen as a Go tool, fetches dependencies
make build      # builds bin/addigyctl
make install    # or: install into $GOBIN / $GOPATH/bin
```

The generated client (`internal/addigy/gen/client.gen.go`) is committed, so `make setup && make build` is enough. You only need `make generate` when the spec or the list of used operations changes (see [Regenerating the client](#regenerating-the-client)).

`make help` lists every target.

## Configuration

Settings are resolved in this order: command-line flags, environment variables, config file, defaults.

| Setting      | Flag              | Environment variable | Config file key |
|--------------|-------------------|----------------------|-----------------|
| API key      | `--api-key`       | `ADDIGY_API_KEY`     | `api_key`       |
| Base URL     | `--base-url`      | `ADDIGY_BASE_URL`    | `base_url`      |
| Organization | `--org-id`        | `ADDIGY_ORG_ID`      | `org_id`        |
| Config file  | `--config-file`   | `ADDIGYCTL_CONFIG`   | n/a             |
| Device columns | `--fact` (on `devices list`) | n/a   | `device_facts`  |
| Timezone     | n/a               | n/a                   | `timezone`      |
| Date format  | n/a               | n/a                   | `date_format`   |
| Table borders | `--borders` / `--no-borders` | n/a  | `borders`       |

The config file is `config.json` in the user config directory: `~/Library/Application Support/addigyctl/config.json` on macOS, `$XDG_CONFIG_HOME/addigyctl/config.json` (or `~/.config/addigyctl/config.json`) on Linux. Create a template with:

```sh
addigyctl config init     # writes the file with mode 0600, refuses to overwrite
addigyctl config path     # prints its location
addigyctl config show     # effective configuration, API key redacted
```

```json
{
  "api_key": "…",
  "base_url": "https://api.addigy.com/api/v2",
  "org_id": "",
  "device_facts": ["serial_number", "device_name", "os_version"],
  "timezone": "Europe/Amsterdam",
  "date_format": "dd-mm-yyyy hh:mm:ss",
  "borders": false
}
```

`org_id` is optional: when it is not set, it is discovered from your policies. `timezone` is an IANA location name (or `CET`/`UTC`) used to render dates, such as `ade tokens`' `TOKEN EXPIRY` and `LAST SCAN` columns; it defaults to the machine's local time zone. `date_format` is a friendly pattern built from `yyyy`, `mm`, `dd`, `hh`, `mm` and `ss` (the second `mm`, next to a `:`, means minutes; the one next to `-`/`/` means month, e.g. `dd-mm-yyyy hh:mm:ss`); it defaults to the notation of the machine's current locale (`LC_ALL`/`LC_TIME`/`LANG`), falling back to day-month-year when that can't be determined. `borders` draws table output as a bordered grid instead of whitespace-separated columns (see [Output formats](#output-formats)); it defaults to `false`, and `--borders`/`--no-borders` always override it. Unknown keys in the file are rejected, and a warning is printed if the file is readable by other users. Prefer the environment variable or the config file over `--api-key`, which ends up in your shell history.

## Usage

```
addigyctl devices  list | get | policies
addigyctl policies list | tree | get
addigyctl facts    list
addigyctl ade      tokens
addigyctl alerts   list
addigyctl config   path | init | show
```

Run any command with `--help` for its flags.

### Policies

Addigy policies are a flat list with a `parent` field. addigyctl builds the hierarchy client-side.

```sh
addigyctl policies list                      # root policies only
addigyctl policies list --parent Acme        # direct sub-policies (ID or name)
addigyctl policies list --all                # every level, flat
addigyctl policies list --name laptop        # name contains text, any level
addigyctl policies list --id <id> --id <id>  # specific policies
addigyctl policies list --sort devices --desc  # busiest policies first
addigyctl policies tree                      # the whole hierarchy
addigyctl policies tree Acme --depth 1 --ids # one branch, with IDs
addigyctl policies get "Acme / Finance"      # one policy in full (JSON)
```

Policies can be referenced by ID, by name, or by a `Parent / Child` path. If a name is ambiguous, the error lists the candidates with their IDs and paths.

The `DEVICES` column (and the `[n]` in the tree) counts the devices in a policy and all of its sub-policies, which is the same set `devices list --policy` returns. Counting needs one bulk fetch of all devices; use `--no-counts` to skip it. `--sort` accepts `name` (default), `id`, `devices`, `children` or `parent`; `--sort devices` needs the counts, so it cannot be combined with `--no-counts`.

### Devices

```sh
addigyctl devices list                         # first 50 devices
addigyctl devices list macbook                 # free-text search (Universal Search)
addigyctl devices list --all                   # every page
addigyctl devices list -f serial_number,device_name,os_version,battery_percent
addigyctl devices list --sort device_name --desc
addigyctl devices list --policy "Acme / Finance"           # located in it or any sub-policy
addigyctl devices list --policy "Acme / Finance" --direct  # located in exactly that policy
addigyctl devices get C02XXXXXXXXX             # agent ID, serial number or device name
addigyctl devices policies <agent-id>          # policies assigned to a device
```

A device has exactly one *location*: its `policy_id` fact. The separate `policy_ids` fact lists every policy that applies to it, which is a different question. `--policy` filters on the location, and adds a `LOCATION` column when sub-policies are included. Addigy cannot filter on a policy subtree itself, so with `--policy` the devices are fetched (a few pages in parallel), filtered, sorted and paginated locally.

### Facts

Device columns are facts. To see which identifiers your tenant has:

```sh
addigyctl facts list
addigyctl facts list battery
addigyctl facts list --sort source
```

`--sort` accepts `identifier` (default), `name`, `type` or `source`.

### ADE tokens

```sh
addigyctl ade tokens                            # every ADE token
addigyctl ade tokens --policy-id <id> --policy-id <id>  # only these policies
addigyctl ade tokens --sort expiry              # tokens closest to expiring first
```

`TOKEN EXPIRY` and `LAST SCAN` are shown in the configured `timezone` and `date_format` (see [Configuration](#configuration); default: the machine's local time zone and date notation). `--json` prints the full token, including `orgid`/`syncing_error`. `--sort` accepts `policy` (default, by full path), `expiry`, `scan`, `disabled` or `synced`.

### Alerts

```sh
addigyctl alerts list                            # unattended and acknowledged alerts (not yet resolved)
addigyctl alerts list --resolved                 # only resolved alerts
addigyctl alerts list --all                      # every status
addigyctl alerts list --muted                    # only muted alerts (any status)
addigyctl alerts list --known-devices            # only alerts for devices that still exist (matches the web GUI)
addigyctl alerts list --category Security        # only this category
addigyctl alerts list --name-contains disk       # name contains text
addigyctl alerts list --sort name                # alphabetical (A-Z)
addigyctl alerts list --desc                     # oldest first, instead of the default newest first
```

Alerts are Addigy's *received* alerts (triggered instances), not alert policy definitions. Status filters use Addigy's own status names directly rather than the web GUI's tab labels, which don't line up 1:1 with them (its "Open" tab, for instance, shows the same alerts as "Unattended"): `--unattended`, `--acknowledged` and `--resolved` are combinable, defaulting to unattended + acknowledged (i.e. not yet resolved) when none are given; `--all` shows every status. `--muted` and `--known-devices` are separate flags with no server-side equivalent, so either forces addigyctl to fetch every page matching the status filter and filter locally, which can be slow combined with `--all`. `SERIAL NUMBER` and `DEVICE NAME` are resolved from each alert's agent ID via one bulk device fetch; an alert whose device no longer exists shows `-` unless `--known-devices` filters it out. That gap is real and can be large: Addigy keeps alert history long after a device is gone (e.g. a "Missing for 30 days" alert that outlives the device itself), which the web GUI silently hides by only showing alerts for devices still in the fleet — `--known-devices` reproduces that view. `--sort` accepts `created` (default), `name`, `level`, `status` or `category`; `created` defaults to newest first and the others to A-Z, and `--desc` reverses whichever default the chosen column has, so it always means "the other order" regardless of `--sort`.

## Output formats

| Flag                      | Output |
|---------------------------|--------|
| (default) or `-o table`   | Aligned table with a summary line |
| `-o csv`                  | CSV with a header row: no footers, empty cells instead of `-`, nothing shortened |
| `-j` / `--json` / `-o json` | The API's JSON, with every field intact |

```sh
addigyctl devices list --all -f serial_number,device_name -o csv > devices.csv
addigyctl policies list --all -o csv
addigyctl policies tree -o csv     # flat: POLICY ID, NAME, PATH, DEPTH, DEVICES
addigyctl devices list -j | jq '.items[].agentid'
```

`policies get` and `config show` only print JSON. The JSON output of `policies list` and `policies tree` gains a `deviceCount` field (unless `--no-counts` is used); everything else is passed through exactly as Addigy returns it.

Warnings and `--debug` request logs go to stderr, so they never end up in piped output.

### Table borders

Tables are whitespace-separated by default. Add `--borders` for a bordered grid, or set `"borders": true` in the config file to make it the default (`--no-borders` always overrides that back off):

```sh
addigyctl policies list --borders
```

```
┌───────────┬────────────┬─────────┬──────────┐
│ POLICY ID │ NAME       │ DEVICES │ CHILDREN │
├───────────┼────────────┼─────────┼──────────┤
│ acme      │ Acme       │ 5       │ 2        │
│ other     │ Other Root │ 1       │ 0        │
└───────────┴────────────┴─────────┴──────────┘
```

## Regenerating the client

```sh
make spec        # re-download api/godoc_swagger.json
make generate    # Swagger 2.0 -> OpenAPI 3, then oapi-codegen
make build
```

The Addigy spec has more than 300 operations; only the ones in `oapi-codegen.yaml` are generated (`GetDevices`, `GetPolicies`, `GetDevicePolicyAssignments`, `GetAvailableFacts`, `GetAdeTokens`). To use another endpoint:

1. Find its `operationId` in `api/godoc_swagger.json`.
2. Add it to `include-operation-ids` in `oapi-codegen.yaml`.
3. Run `make generate`.
4. Add a small method to `internal/addigy/api.go` that calls the new generated `…WithResponse` function.

The wrapper in `internal/addigy` only decodes the fields the CLI needs and keeps each item's raw JSON, which is what `--json` prints.

## Project layout

```
cmd/addigyctl/           Kong commands (devices, policies, facts, ade, config)
internal/addigy/         thin wrapper over the generated client
internal/addigy/gen/     generated client and models (do not edit)
internal/config/         config file handling
internal/output/         table, CSV and JSON rendering
tools/swagger2openapi/   converts the Swagger 2.0 spec to OpenAPI 3
api/                     the Addigy spec (openapi3.json is derived and git-ignored)
```

## Development

```sh
make test        # go test ./...
```

The tests run against local fake HTTP servers, so they need neither network access nor an API key.
