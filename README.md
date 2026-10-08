# addigyctl

A small command line tool for the [Addigy](https://addigy.com) v2 API of a single tenant. It queries devices, policies, facts, ADE tokens, alerts, system events and Smart Software, and it can publish new Smart Software versions from item folders kept as code, and delete versions after backing them up (see [Smart Software](#smart-software)), and delete uploaded files nothing uses (see [Files](#files)). Publishing and deleting are its only write operations; everything else is read-only.

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
| Software root | `--software-root` | `ADDIGYCTL_SOFTWARE_ROOT` | `software_root` |

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
  "borders": false,
  "software_root": "~/src/addigy-software",
  "backup_dir": ""
}
```

`org_id` is optional: when it is not set, it is discovered from your policies. `timezone` is an IANA location name (or `CET`/`UTC`) used to render dates, such as `ade tokens`' `TOKEN EXPIRY` and `LAST SCAN` columns; it defaults to the machine's local time zone. `date_format` is a friendly pattern built from `yyyy`, `mm`, `dd`, `hh`, `mm` and `ss` (the second `mm`, next to a `:`, means minutes; the one next to `-`/`/` means month, e.g. `dd-mm-yyyy hh:mm:ss`); it defaults to the notation of the machine's current locale (`LC_ALL`/`LC_TIME`/`LANG`), falling back to day-month-year when that can't be determined. `borders` draws table output as a bordered grid instead of whitespace-separated columns (see [Output formats](#output-formats)); it defaults to `false`, and `--borders`/`--no-borders` always override it. `software_root` is the folder holding Smart Software item folders (see [Smart Software](#smart-software)); `~` is expanded. `backup_dir` is where `smart-software delete` writes its backups and `files delete` its deletion logs; it defaults to `backups` next to the config file, and `~` is expanded. Unknown keys in the file are rejected, and a warning is printed if the file is readable by other users. Prefer the environment variable or the config file over `--api-key`, which ends up in your shell history.

## Usage

```
addigyctl devices  list | get | policies
addigyctl policies list | tree | get
addigyctl facts    list
addigyctl ade      tokens
addigyctl alerts   list
addigyctl events   list
addigyctl smart-software list | get | export | new-version | delete
addigyctl files    list | find | delete
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
addigyctl policies list --sort devices       # busiest policies first
addigyctl policies tree                      # the whole hierarchy
addigyctl policies tree Acme --depth 1 --ids # one branch, with IDs
addigyctl policies get "Acme / Finance"      # one policy in full (JSON)
```

Policies can be referenced by ID, by name, or by a `Parent / Child` path. If a name is ambiguous, the error lists the candidates with their IDs and paths.

The `DEVICES` column (and the `[n]` in the tree) counts the devices in a policy and all of its sub-policies, which is the same set `devices list --policy` returns. Counting needs one bulk fetch of all devices; use `--no-counts` to skip it. `--sort` accepts `name` (default), `id`, `devices`, `children` or `parent`; `--sort devices` needs the counts, so it cannot be combined with `--no-counts`. `devices` and `children` default to most first (a count, where more is usually more interesting); the rest default to A-Z. `--desc` reverses whichever default the chosen column has, so it always means "the other order" regardless of `--sort`.

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

### System events

```sh
addigyctl events list                            # last 24 hours, newest first
addigyctl events list --since 7d                 # last 7 days
addigyctl events list --since 2026-01-01T00:00:00Z --until 2026-01-02T00:00:00Z
addigyctl events list --level warning            # only this level
addigyctl events list --action failed            # action name contains text
addigyctl events list --oldest                   # oldest first, instead of the default newest first
```

System events are Addigy's audit log: who or what (a device, a user, the platform itself, ...) did what, to what, and whether it succeeded. `--since`/`--until` accept an RFC3339 timestamp or a duration before now (`24h`, `30m`, `7d`); `--since` is required by the endpoint and defaults to `24h`, `--until` defaults to now. `--level` and `--action` are free-text filters against the event's level and action name. There is only one sort dimension (time); it defaults to newest first, and `--oldest` reverses that. Addigy's total event count is capped at a round number (commonly 10000) rather than an exact count once a query matches that many or more, so treat a `total` that round as "at least that many," not exact.

### Smart Software

Smart Software items can be kept as code: one folder per item, under version control, from which new versions are published. addigyctl defines the folder format and knows nothing about specific software; what goes in the folders is up to you.

```sh
addigyctl smart-software list                  # one row per version, archived ones hidden
addigyctl smart-software list --name zoom --archived
addigyctl smart-software get Airtame           # latest version as JSON
addigyctl smart-software export Airtame --placeholders   # → <software_root>/airtame
addigyctl smart-software new-version Airtame --to 4.16.0 --dry-run   # finds Airtame-4.16.0.pkg itself
addigyctl smart-software new-version Airtame --to 4.16.0
addigyctl smart-software delete <instruction_id> [<instruction_id> ...] --dry-run   # backs up, then deletes
addigyctl smart-software delete --archived --name zoom   # every archived version of the matching items
addigyctl files find --name Airtame-4.16       # look up uploads by name (loose search) or --md5
```

**Identity.** Every version of an item shares its `identifier` (`<name>-<uuid>`); each version has its own `instruction_id`. Commands take a version's `instruction_id`, or an item's `identifier` or name, which mean its latest version: the highest version number, compared naturally (`9.2` < `10.0`).

**Item folders.** `export` writes one:

```
airtame/
  item.yaml              settings: category, priority, conditions, profiles, icon, downloads, ...
  install.sh             installation script
  condition.sh           condition script
  remove.sh              remove script
  .addigyctl-state.yaml  the Addigy version the folder last matched; commit it, don't edit it
```

An empty script has no file. `item.yaml` holds only the settings you can edit; fields Addigy manages itself (label, provider, ...) are left out. addigyctl only touches these files, so the folder can hold others (build or test configuration, say). Without `--dir` or `--item`, `export` names the folder after the item in the software root; it never overwrites another item's folder, and only overwrites its own with `--force`.

**Downloads.** `item.yaml` lists a version's files by name pattern under `version_downloads`, e.g. `Airtame-{{.Version}}.pkg`; publishing 4.16.0 uses the upload named exactly `Airtame-4.16.0.pkg`. The list can have any number of patterns, including none; edit it when a release needs other files. Addigy allows several uploads with the same name: identical ones (same MD5) are fine and the newest is used, different ones are refused with a list to pick from. `--file` replaces the list for one run and takes an Addigy file ID, a local file (matched to the upload with the same content), or an exact file name. Files every version needs can go under `downloads` by file ID. `export --placeholders` turns the exported version's download names into patterns when they contain the version.

**Placeholders.** Scripts and `item.yaml` values can use `{{.Version}}` (the version being published) and `{{.Filename}}` (the name of the version's first download), in Go template syntax; quote them in YAML (`version: "{{.Version}}"`). A version number usually appears in both: an install script's `VERSION=` and the "install if older than" check in `predefined_conditions.app_exists.version`. `export --placeholders` replaces the exported version with `{{.Version}}` and reports how many it replaced per file. That is plain text matching, so review it: a version like `1.0` also matches `<?xml version="1.0"?>`.

**Publishing.** `new-version` renders the folder for the new version, adds the `--file` downloads (plus any listed under `downloads:` in `item.yaml`), shows the change against the current version, and asks for confirmation (`--dry-run` only shows it; `--yes` skips the question; without a terminal it refuses rather than ask). The version is `--to`, as `--version` prints addigyctl's own version. It refuses a version that already exists, and a download that isn't uploaded or is ambiguous; with `--force` it publishes anyway when:

- the version is not higher than the current one,
- it would have no downloads while the current version has some (usually a forgotten `version_downloads` entry), or
- the item was changed in Addigy outside the folder: a version published elsewhere, or the current version edited in the Addigy UI. The state file tells these apart from changes made in the folder, which are what you are publishing.

For automation, `--wait-for-files 2h` waits (checking every 30 seconds) for downloads a person still has to upload, instead of failing right away; an ambiguous upload still fails at once. Combined with `--file <local build>`, it waits for the upload with exactly that content.

A new version does nothing until it is assigned to policies, which addigyctl does not do. The v2 API cannot upload files: upload installers in the Addigy UI first.

**Deleting.** `delete` removes versions, named by their `instruction_id` only (a name or `identifier` would mean whichever version is latest), or picked with `--archived --name <text>`: every archived version of the items whose name contains the text (`--name` is required, so it never means every archived version at once). The items' other versions stay, and so do their downloads in Addigy's file storage (`files list --unused` shows the ones nothing uses any more). It fetches every version first, so an unknown ID stops it with nothing deleted, then shows them (oldest first per item) with their downloads and asks for confirmation once (`--dry-run` only shows them; `--yes` skips the question; without a terminal it refuses rather than ask). A failed delete doesn't stop the others; they are all reported at the end. The API key needs Addigy's "Delete Smart Software" permission; a refusal of the key itself (401 or 403) stops the run.

Before deleting a version, it writes its backup: the version as Addigy returns it, with its scripts, settings and each download's ID, name, size and MD5, but not the files themselves. Backups go to `<backup_dir>/<item>/<version>-<instruction_id>-<UTC time>.json` (`--backup-dir` overrides the config file), readable only by you, since scripts can hold license keys or tokens. If a backup can't be written, that version and the ones after it are not deleted; if a delete fails, its backup is removed again. `--no-backup` skips the backups. There is no restore command yet.

A version that is assigned to policies is deleted all the same: Addigy silently removes it from those policies, and the backup does not record them. The v2 API doesn't show these assignments reliably, so addigyctl can't check first; look in the Addigy UI before deleting a version that may be in use.

### Files

```sh
addigyctl files list                     # every uploaded file and what uses it, newest first
addigyctl files list --unused --sort size   # cleanup candidates, largest first
addigyctl files list --name helloworld -o csv
addigyctl files find --md5 10da1c00044e49763b82e02aeab98b30
addigyctl files delete --unused --name citrix --dry-run
addigyctl files delete <file-id> <file-id>
```

`files list` shows every file in Addigy's file storage with the number of places it is used and what uses it, as Addigy itself tracks it: Smart Software versions (their downloads and uploaded icons, archived versions included), Self Service and policies. A file with no uses is used nowhere, which makes it a candidate for cleaning up; Addigy refuses to delete a file that is in use. With `--unused` the table leaves out the then empty `USES` and `USED BY` columns (CSV keeps them).

`files delete` deletes files nothing uses, for good: Addigy keeps no copy, and the API can't download them first. Name the files by ID, or pick them with `--unused` plus `--name` and/or `--before YYYY-MM-DD` (one is required, so it never means every unused file at once). It refuses any named file that is in use, shows the files (oldest first) with their total size and asks for confirmation (`--dry-run` only shows them; `--yes` skips the question; without a terminal it refuses rather than ask). Right before deleting it checks the usage again and skips a file that has come into use since. A failed delete doesn't stop the others; they are all reported at the end. Only a refusal of the API key itself (401 or 403) stops the run, since it would refuse every file. The API key needs Addigy's "Delete Files" permission.

Each run writes a deletion log, readable only by you, to `<backup_dir>/files/deleted-<UTC time>.json`: every file's ID, name, size, MD5 and upload time, and whether it was deleted. It is not a backup, but it shows what was removed, and the MD5 recognizes a local copy (`md5 -q <file>`). If the log can't be written, nothing is deleted. Items Addigy has no name for show as their type and ID (e.g. `policy b8763068-…`). `--sort` takes `created` (default, newest first), `size` and `uses` (largest and most first) or `name`; `--desc` reverses that. Tables shorten long values, including the very long IDs of files uploaded before 2020; `-o csv` and `-j` keep them whole.

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

`policies get`, `smart-software get` and `config show` only print JSON. `smart-software export`, `new-version` and `delete`, and `files delete` print a summary, or with `-j` a stable JSON result for scripts (`new-version -j` and both `delete -j`s write their preview to stderr). The JSON output of `policies list` and `policies tree` gains a `deviceCount` field (unless `--no-counts` is used), and that of `files list` a `usages` array per file, as Addigy returns it; everything else is passed through exactly as Addigy returns it.

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

The Addigy spec has more than 300 operations; only the ones listed in `oapi-codegen.yaml` are generated. To use another endpoint:

1. Find its `operationId` in `api/godoc_swagger.json`.
2. Add it to `include-operation-ids` in `oapi-codegen.yaml`.
3. Run `make generate`.
4. Add a small method to `internal/addigy/api.go` that calls the new generated `…WithResponse` function.

The wrapper in `internal/addigy` only decodes the fields the CLI needs and keeps each item's raw JSON, which is what `--json` prints.

## Project layout

```
cmd/addigyctl/           Kong commands
internal/addigy/         thin wrapper over the generated client
internal/swfolder/       the Smart Software item folder format and placeholders
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
