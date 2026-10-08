# Design: Smart Software commands

Status: agreed design; `list`, `get`, `export`, `new-version` and `files find` implemented and verified on a live tenant (Oct 2026). Assign/unassign not yet built.

## Context

The intended workflow keeps Smart Software definitions as code in a separate repository
(a "software repo"). That repo is the source of truth for every Smart Software item;
changes are made there and published to Addigy with addigyctl.

addigyctl stays a **generic** Addigy tool. It must not contain any knowledge of specific
software, customers, policies or any particular software repo. It provides generic
operations and a generic on-disk format for a Smart Software item; the software repo
fills that format.

This is the first time addigyctl writes to Addigy. Until now it is read-only (see README).

## Decisions

1. **Generic only.** No package-specific logic in addigyctl.
2. **On-disk folder format belongs to addigyctl.** One folder = one Smart Software item.
   addigyctl reads/writes only its own files in the folder and ignores everything else
   (the software repo keeps e.g. `check.toml` and `build.yaml` there for its pipeline).
3. **Two ways to point at an item folder.** `--dir <path>` is a plain path to one item
   folder and always works. Alternatively, a `software_root` setting (config key
   `software_root`, env `ADDIGYCTL_SOFTWARE_ROOT`, flag `--software-root`) names the
   folder that contains item folders; then `--item <name>` resolves to
   `<software_root>/<name>`. `--dir` and `--item` are mutually exclusive. addigyctl still
   knows nothing about what's inside; it only learns where to look.
4. **Strict drift check.** Publishing refuses when the item's current version in Addigy
   differs from the folder (scripts, conditions, settings). `--force` overrides.
5. **Placeholders** in scripts and in every text value of `item.yaml`, filled in at
   publish time: `{{.Version}}` and `{{.Filename}}` (Go `text/template`). Unknown
   placeholders, and placeholders without a value (`{{.Filename}}` with no file), are an
   error. `item.yaml` is rendered per value after parsing, so a value can never break the
   YAML; values holding a placeholder must be quoted. Needed beyond scripts because e.g.
   `predefined_conditions.app_exists.version` holds the version (9 of 64 active items on
   the tenant checked; 44 of 64 have the version somewhere).
6. **Version = the software's own version.** The Smart Software version in Addigy is
   the version of the software in the shipped artifact (e.g. the app's
   CFBundleShortVersionString), passed in with `--to` (`--version` is taken: it prints addigyctl's own version). addigyctl does not invent
   or bump versions.
7. **No assumption about the artifact type.** An item can have zero or more downloads
   (pkg, dmg, zip, app, …); the item's scripts decide how they are used.
8. **Installers are not stored in the folder.** Files are referenced by Addigy file ID,
   found with `files find`.
9. **Publishing ≠ accepting.** A new version is inert in Addigy until it is assigned to
   policies. addigyctl offers assign/unassign commands for humans; automation in the
   software repo never calls them.
10. **Write safety.** Every write command shows what it will change and asks for
    confirmation; `--dry-run` shows the change without doing it; `--yes` skips the
    prompt for scripted use. `-j` output stays stable so other tools can parse it.
11. **`--from-current`** (copy the latest version, override only given fields) is fine
    inside addigyctl as a generic convenience for manual updates.

## Commands

```
addigyctl files find --md5 <hash> | --name <text>
addigyctl smart-software list [--name <text>] [--archived]
addigyctl smart-software get <id>
addigyctl smart-software export <ref> [--dir <path> | --item <name>] [--placeholders] [--force]
addigyctl smart-software diff (--dir <path> | --item <name>)
addigyctl smart-software new-version (--dir <path> | --item <name>) --to <v> --file <file-id>... [--force] [--dry-run] [--yes]
addigyctl smart-software new-version <id> --from-current --to <v> [--file <file-id>...] [--dry-run] [--yes]
addigyctl policies assign-software <policy> <smart-software-id> [--dry-run] [--yes]
addigyctl policies unassign-software <policy> <smart-software-id> [--dry-run] [--yes]
```

`software_root` follows the existing settings order (flag, env, config file); `~` is
expanded. `config show` should display it.

All read commands support `-o table|csv|json` / `-j` like the existing commands.
Policies are referenced the same way as elsewhere (ID, name or `Parent / Child` path).

- `files find --md5` lets a pipeline hash a local artifact and find the file it was
  uploaded as, without copying IDs by hand.
- `export` creates the folder from an existing item (one-time bootstrap).
- `diff` compares folder vs. the item's current version in Addigy; exit code non-zero
  when they differ (usable in scripts).
- `new-version --dir` renders placeholders, runs the drift check, and creates a new
  version from the folder plus `--to` and `--file`.

## Folder format

Implemented in `internal/swfolder`.

```
<item>/
  item.yaml        # everything that is not a script
  install.sh       # installation_script
  condition.sh     # condition
  remove.sh        # remove_script
```

An empty script has no file. `item.yaml` holds exactly the editable fields of
`smart_software.CreateSmartSoftwareRequest`, under the API's own key names; server-managed
fields (`label`, `name`, `provider`, `type`, `organization_id`, `user_email`, `public`,
`tcc_version`, ...) are left out. Unknown keys are rejected when reading.

```yaml
# Exported by addigyctl from Example App, version 1.2.0 (instruction_id ...).
# That version's downloads, deliberately not copied into downloads below. ...
#   <file-id>  ExampleApp-1.2.0.pkg

identifier: Example App-<uuid>   # shared by every version; set by export
base_identifier: Example App
category: Utilities
description: ""
priority: 5
run_on_success: false
status_on_skipped: finished
predefined_conditions: {...}     # app_exists, file_exists, os_version, ... as in the API
profiles: []                     # PPPC / system extension / service management, as in the API
software_icon:                   # an uploaded file (cloud-storage) or an image URL (web)
  id: <file-id or URL>
  provider: cloud-storage
downloads: []                    # files every version needs (by file id)
version_downloads:               # files each version needs, by exact name; any number, edit per release
  - "Example App-{{.Version}}.pkg"
```

`version_downloads` is addigyctl's own and never sent to Addigy. Publishing renders each
pattern for the new version and uses the upload with exactly that name. Several uploads
with that name and the same MD5 are harmless (the newest is used); different MD5s are
refused, listing them. `--file` replaces the list for one run: a file ID, a local file
(matched by MD5) or an exact name. Files are resolved after the version and drift checks,
so a missing upload never hides those.

Per-version values (`version`, the version's installer) are not stored in `item.yaml`;
they come from the `new-version` flags. Export cannot tell a version's per-version
downloads from the ones every version needs, so it leaves `downloads` empty and lists the
exported version's downloads in the header comment; with `--placeholders`, those named
after the version become `version_downloads` patterns.

`export` without `--dir` or `--item` names the folder after the item:
`<software_root>/<slug>`, where the slug is the name lowercased with other characters
than letters, digits, `.` and `_` turned into `-` (`Akvo/user-config` →
`akvo-user-config`). Writing never replaces a folder holding a different item (another
`identifier`), even with `--force`.

Export always escapes `{{` in Addigy's content (`{{"{{"}}`), so the folder renders back
to exactly what Addigy has. `--placeholders` also replaces each standalone occurrence of
the exported version (not inside a longer version: `4.1` is left alone in `4.15`) with
`{{.Version}}`, and reports the count per file. It is plain text matching and needs
review: a version like `1.0` also matches `<?xml version="1.0"?>`. Verified on a live
tenant: every active item (64) exported with `--placeholders` reads back and renders with
its own version to exactly the API's values.

A command's `<ref>` is a version's `instruction_id` (a bare UUID), or an item's
`identifier` or name (`base_identifier`, case-insensitive), which resolve to the item's
latest version. "Latest" is the highest `version` by natural comparison (`9.2` <
`10.0`); on a live tenant this matched both the API's order and the newest download date
for every item with several versions.

## API (Addigy v2, from api/godoc_swagger.json)

Operations to add to `include-operation-ids` in `oapi-codegen.yaml`:

| operationId | Method + path | Used by |
|---|---|---|
| `GetSmartSoftwareItems` | `POST /oa/smart-software/query` | list, export, diff |
| `GetSmartSoftware` | `GET /o/{organization_id}/smart-software/{id}` | get, export, diff |
| `CreateSmartSoftware` | `POST /o/{organization_id}/smart-software` | restore (when no version of the item is left); later: create from folder |
| `CreateSmartSoftwareNewVersion` | `POST /o/{organization_id}/smart-software/{id}/new-version` | new-version |
| `DeleteSmartSoftware` | `DELETE /o/{organization_id}/smart-software/{id}` | delete (one version; needs the "Delete Smart Software" permission) |
| `GetOrganizationFiles` | `POST /oa/files/query` (filters: `md5_hash`, `search_term`, `ids`) | files find |
| `GetOrganizationFile` | `GET /oa/files/{file_id}` | files find, export |
| `GetTrackedFiles` | `POST /files/usage` (body: `file_ids`; 687 IDs in one call works) | files list (Addigy's own usage tracking: Smart Software downloads and uploaded icons incl. archived versions, Self Service, policies) |
| `DeleteOrganizationFile` | `DELETE /o/{organization_id}/files/{file_id}` (204; long pre-2020 IDs work) | files delete (needs the "Delete Files" permission) |
| `AssignSmartSoftwareToPolicy` | `POST /o/{organization_id}/policies/{policy_id}/smart-software/{asset_id}` | policies assign-software |
| `UnassignSmartSoftwareFromPolicy` | `DELETE …/smart-software/{asset_id}` | policies unassign-software |

Relevant schemas: `instructions_service.CustomSoftware` (item as returned),
`smart_software.CreateSmartSoftwareRequest`, `smart_software.UpdateSmartSoftwareRequest`
(new-version body), `smart_software.Filter` (query: `ids`, `identifier`, `name_contains`,
`archived`), `smart_software.Download` (`{id}`), `file_manager_service.OrganizationFile`.

There is **no file upload endpoint** in v2 (v1, which had one, was removed on
31 March 2026). Uploading artifacts stays manual in the Addigy UI.

## Facts confirmed

- A new Smart Software version does nothing until it is assigned: it shows up in the
  policies screen and policies must be selected explicitly.
- Identity (verified against a live tenant): every version of an item shares
  `identifier` (`<base_identifier>-<uuid>`); `instruction_id` is unique per version.
  `CustomSoftware` has no `id` field. `GET /smart-software/{id}` takes the
  `instruction_id` (an `identifier` gives a 500). An unknown id is also a 500 whose
  nested `error_chain` says "custom software not found".
- `POST /oa/smart-software/query` returns one row per version, archived ones included
  unless `archived` is set; the `identifier` filter returns all versions of one item.
  `per_page` is capped at 100. Its `sort_direction` is not inverted (unlike alerts).
- `POST /oa/files/query` 400s without `page`; `per_page` is capped at 100;
  `md5_hash` matches exactly. `search_term` is a loose, case-insensitive search, not a
  substring match: `helloworld-1.0.0` also returns e.g. `Nudge-1.0.0....pkg`.
- On a Mac, a version's downloads are at
  `/Library/Addigy/ansible/packages/<base_identifier> (<version>)/<filename>` (verified
  end to end with a test item on a test policy). Scripts must quote that path: it holds
  a space and parentheses. Uploaded files are not executable; scripts must `chmod` them.
- `new-version` (verified live, Hello World 1.0.0 → 1.1.0): the path's `{id}` is the
  current version's `instruction_id`; it needs the API key permission "Create Smart
  Software" (403 without). Addigy sets `name` (`<base> (<version>)`) itself. `label` is
  Addigy's too, read-only (not even editable in the UI), and ignored in requests. It
  records lineage: a first version is labelled `Custom Software - <its own name>`, a later
  one `Custom Software - <name of the version it was created from>` (1.2.0 created from
  1.1.0: "… (1.1.0)"). True for every multi-version item on the tenant checked, including
  versions made in the UI. addigyctl never sends it.
- Addigy allows editing a version in place (same `instruction_id`); the audit log
  records it as "updated custom software".

## Open questions / verify during implementation

- Which id `asset_id` in the assign endpoints expects (`identifier` or
  `instruction_id`); see "Facts confirmed" for how they relate.
- Whether Addigy accepts two versions with the same `version` string under one item
  (matters for script-only fixes, given decision 6). How to version script-only fixes
  is still undecided.
- How `software_icon` is set (it references an uploaded file). Export keeps `id` and
  `provider`, the fields of `instructions_service.SoftwareIconRequest`.
- Which `CustomSoftware` fields are server-managed and must be excluded from export/diff
  (e.g. `organization_id`, `user_email`, `instruction_id`, `provider`, `public`).
