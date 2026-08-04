# Switch Codex

Switch Codex is a macOS-first Tauri app for managing multiple Codex `auth.json` profiles.

## Features

- Configure multiple Codex account names.
- Associate each account with an `auth.json` file stored under this project in `data/accounts/<account-id>/auth.json`.
- Mark one account as active and atomically replace `~/.codex/auth.json`.
- Switch accounts from the app window, the macOS application menu, or the macOS menu bar status item.
- Validate imported auth files and keep the previous active file at `~/.codex/auth.json.switch-codex.bak`.
- Query each saved ChatGPT Codex account's current subscription usage windows.
- Aggregate local input, cached input, cache-write, output, and reasoning token counts from `~/.codex/sessions`.
- Estimate an API-equivalent USD cost with prices refreshed from the [official OpenAI pricing page](https://developers.openai.com/api/docs/pricing).

## Usage Statistics

Open the **用量统计** tab to see:

- Independent short-window and weekly quota percentages for each saved account.
- Local token totals by model and day.
- An API-equivalent cost estimate based on OpenAI's standard per-token prices.

The quota request uses the same read-only ChatGPT Codex usage endpoint and account header as the official Codex client. Credentials are read in the Rust backend and are never returned to the renderer or written to logs. Session files are parsed locally; only timestamps, model names, and token counters are retained and returned.

ChatGPT/Codex paid plans are subscription products, so the displayed USD amount is a comparison estimate rather than an actual bill. Models without a published price are excluded from the estimate. The last successfully fetched official price catalog is cached in the app data directory for offline use.

Codex session JSONL files currently do not identify the user/account attached to each token event. Account quota cards are therefore per-account, while historical local token and cost totals are combined across accounts on this machine.

## Run

```bash
nvm use
npm install
npm run dev
```

## Build Packages

```bash
npm run build:mac:arm
npm run build:mac:x64
npm run build:win:x64
```

Build outputs are written to `src-tauri/target/<target>/release/bundle/`.

Mac arm64 and x64 packages should be built on macOS. Windows amd64 packages can be built on Windows or via GitHub Actions.

## GitHub Actions

- Every pull request runs `npm ci` and `npm run lint`.
- Pushing code to `master` (or manual trigger) automatically checks the latest Git Tag, increments the patch version (e.g. `v1.0.8` -> `v1.0.9`), pushes the new tag to GitHub, builds packages across targeted platforms (macOS arm64, macOS x64, Windows x64), and attaches the assets to GitHub Releases.

## Data Location

By default, account data is stored in:

```text
data/
  accounts.json
  accounts/
    <account-id>/auth.json
```

You can override this location with `CODEX_SWITCH_DATA_DIR` if needed.

## Platform Notes

The current project is macOS-first with Windows support. The core file-copying logic uses Rust backend APIs and is kept platform-neutral.

## License

This project is licensed under the [MIT License](LICENSE).

