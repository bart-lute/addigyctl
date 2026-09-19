# addigyctl

A small command line tool to query the [Addigy](https://addigy.com) v2 API for a single tenant. It is read-only and covers devices, policies and facts.

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
  "device_facts": ["serial_number", "device_name", "os_version"]
}
```

`org_id` is optional: when it is not set, it is discovered from your policies. Unknown keys in the file are rejected, and a warning is printed if the file is readable by other users. Prefer the environment variable or the config file over `--api-key`, which ends up in your shell history.

## Usage

```
addigyctl devices  list | get | policies
addigyctl policies list | tree | get
addigyctl facts    list
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
addigyctl policies tree                      # the whole hierarchy
addigyctl policies tree Acme --depth 1 --ids # one branch, with IDs
addigyctl policies get "Acme / Finance"      # one policy in full (JSON)
```

Policies can be referenced by ID, by name, or by a `Parent / Child` path. If a name is ambiguous, the error lists the candidates with their IDs and paths.

The `DEVICES` column (and the `[n]` in the tree) counts the devices in a policy and all of its sub-policies, which is the same set `devices list --policy` returns. Counting needs one bulk fetch of all devices; use `--no-counts` to skip it.

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
```

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

## Regenerating the client

```sh
make spec        # re-download api/godoc_swagger.json
make generate    # Swagger 2.0 -> OpenAPI 3, then oapi-codegen
make build
```

The Addigy spec has more than 300 operations; only the ones in `oapi-codegen.yaml` are generated (`GetDevices`, `GetPolicies`, `GetDevicePolicyAssignments`, `GetAvailableFacts`). To use another endpoint:

1. Find its `operationId` in `api/godoc_swagger.json`.
2. Add it to `include-operation-ids` in `oapi-codegen.yaml`.
3. Run `make generate`.
4. Add a small method to `internal/addigy/api.go` that calls the new generated `…WithResponse` function.

The wrapper in `internal/addigy` only decodes the fields the CLI needs and keeps each item's raw JSON, which is what `--json` prints.

## Project layout

```
cmd/addigyctl/           Kong commands (devices, policies, facts, config)
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
