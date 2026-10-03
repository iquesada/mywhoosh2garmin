# mywhoosh2garmin

A command-line tool, written in Go, that automatically syncs workouts recorded in [MyWhoosh](https://www.mywhoosh.com/) to Garmin Connect. MyWhoosh already uploads sessions to Strava but not to Garmin; this project closes that gap.

> **Note:** neither MyWhoosh nor Garmin offers a public API for this. The tool uses the same private APIs as their apps (see [ADR 0002](adr/0002-source-and-destination-connectors.md)), so it may break when either service changes.

## Requirements

- Go 1.24 or later to build.
- A MyWhoosh account and a Garmin Connect account.
- A browser on the machine where you run `login` (only once).

## Build

```sh
go build -o bin/mywhoosh2garmin ./cmd/mywhoosh2garmin
```

## Usage

1. Sign in to Garmin Connect. Your browser opens Garmin's own sign-in page; the tool never sees your Garmin password. The session is stored in your user config directory and renewed automatically.

   ```sh
   bin/mywhoosh2garmin login
   ```

2. Provide your MyWhoosh credentials through the environment:

   ```sh
   export MYWHOOSH_EMAIL="you@example.com"
   export MYWHOOSH_PASSWORD="..."
   ```

3. Check what would be synced, then sync:

   ```sh
   bin/mywhoosh2garmin list
   bin/mywhoosh2garmin sync -dry-run
   bin/mywhoosh2garmin sync
   ```

`sync` checks the 10 most recent MyWhoosh activities by default (`-limit` changes it), uploads the ones that are not synced yet, and records them so they are never uploaded twice. It is safe to run repeatedly, for example from cron.

Set `MYWHOOSH2GARMIN_CONFIG_DIR` (or pass `-config-dir`) to store the Garmin session and the sync state somewhere other than the default user config directory.

## Development

```sh
go vet ./...
go test ./...
```

The code has no third-party dependencies. The layout is:

- `cmd/mywhoosh2garmin`: the command-line interface.
- `internal/activity`: domain types and the `Source` and `Destination` interfaces.
- `internal/mywhoosh`: source connector for MyWhoosh.
- `internal/garmin`: destination connector for Garmin Connect (sign-in, token refresh, upload).
- `internal/syncer`: the sync logic, independent of any connector.
- `internal/state`: the record of synced activities.

## Architecture decisions

All development is driven by Architecture Decision Records (ADRs), stored in the [`adr/`](adr/) folder. Read them to understand why the project is built the way it is, and add a new ADR before making any significant design change.

## Language policy

All project content must be written in English: code, code comments, commit messages, documentation, ADRs, issues and any other collaboration.

## License

This project is licensed under the [MIT License](LICENSE).
