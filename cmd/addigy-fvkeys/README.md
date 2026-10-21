# addigy-fvkeys

Exports the FileVault recovery keys of the devices in an Addigy policy to an Excel file.

> [!WARNING]
> **Unofficial.** Addigy's public API can't return FileVault recovery keys. This program uses the internal API of Addigy's web app instead (`GET https://app.addigy.com/api/mdm/devices/filevault/keys`), with your browser session. That endpoint is undocumented: Addigy can change or remove it at any time, and this program then stops working.
>
> **Temporary.** Remove this program once Addigy's public API supports retrieving FileVault recovery keys.
>
> **Logged.** Addigy most likely records every key you view, under your name, just as when you open a key in the web app. One export means one view per device.

## What you get

```sh
addigy-fvkeys --policy "Acme / Finance"            # the policy and its sub-policies
addigy-fvkeys --policy "Acme / Finance" --direct   # only devices located directly in it
addigy-fvkeys --policy "Acme / Finance" finance-keys.xlsx
```

One Excel file with the columns **Device Name, Serial Number, Policy, Created, Key**. A device with several keys (after a rotation, say) gets one row per key, newest first. Created is when Addigy received the key, shown in addigyctl's `timezone` and `date_format` (see the [main README](../../README.md#configuration)), else in your Mac's time zone and Excel's regional format. A device without a key gets one row with an empty Key, so you can see what's missing.

The endpoint doesn't say which key is the device's current one. The newest is the obvious candidate, and the web app marks it "Enabled" in the cases seen so far, but check the web app when it matters.

Without a file name, the file is `fvkeys-<policy>-<date>.xlsx` in the export folder: `--export-dir`, `ADDIGYCTL_EXPORT_DIR` or `export_dir` in addigyctl's config file, else `~/addigy-exports` (created readable only by you). The file itself is readable only by you, and an existing file is not overwritten unless you pass `--force`.

Keys are never printed. The program shows its progress, warns about devices it couldn't fetch keys for (they get a row with an empty Key), and ends with a summary.

## What it needs

1. **An Addigy API key**, the same one addigyctl uses: `ADDIGY_API_KEY` or `api_key` in addigyctl's config file (see the [main README](../../README.md#configuration)). It finds the policy and its devices.
2. **Your Addigy session token**: the value of the `prod_auth_token` cookie of a logged-in Addigy session in your browser. It fetches the keys, with the same rights you have in the web app.

### Getting the session token

Log in to [app.addigy.com](https://app.addigy.com) first.

**Chrome**

1. Open DevTools: <kbd>⌥⌘I</kbd> (macOS) or <kbd>F12</kbd>.
2. Open the **Application** tab (behind **»** if the window is narrow).
3. In the sidebar, under **Storage → Cookies**, select `https://app.addigy.com`.
4. Click the row named `prod_auth_token`, double-click its **Value** and copy it (<kbd>⌘C</kbd>).

**Firefox**

1. Open the developer tools: <kbd>⌥⌘I</kbd> (macOS) or <kbd>F12</kbd>.
2. Open the **Storage** tab (behind **»** if the window is narrow).
3. In the sidebar, under **Cookies**, select `https://app.addigy.com`.
4. Click the row named `prod_auth_token`, double-click its **Value** and copy it (<kbd>⌘C</kbd>).

Copy only that cookie's value. `prod_original_auth_token` and other cookies aren't needed.

### Giving it to addigy-fvkeys

Simplest: just run `addigy-fvkeys` and paste the token when it asks. The input is hidden and nothing ends up in your shell history.

For several runs in a row, put it in an environment variable for the current terminal only, and remove it afterwards:

```sh
read -s "ADDIGY_SESSION_TOKEN?Token: "; echo; export ADDIGY_SESSION_TOKEN   # zsh
addigy-fvkeys --policy "Acme / Finance"
addigy-fvkeys --policy "Acme / Sales"
unset ADDIGY_SESSION_TOKEN
```

(In bash: `read -s -p "Token: " ADDIGY_SESSION_TOKEN`.)

The token is your logged-in session: anyone who has it can act as you in Addigy until it expires. Don't save it in a file or a script, don't pass it as a command-line argument, and don't paste it into a chat or a ticket.

When the token has expired or is wrong, addigy-fvkeys stops at the first device, writes no file, and says so. Copy a fresh token and run it again. Logging out of Addigy may end the session, and with it the token, straight away.

## Building

`make build` in the repo root builds `bin/addigy-fvkeys` next to `bin/addigyctl`.
