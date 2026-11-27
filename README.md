# jobctl

[![CI](https://github.com/ivolejon/jobctl/actions/workflows/ci.yml/badge.svg)](https://github.com/ivolejon/jobctl/actions/workflows/ci.yml)

`jobctl` runs shell jobs from a version-controlled `jobs.toml` on Linux and macOS. Run a job on demand or schedule it with systemd or launchd. Both use the same execution path for locking, timeouts, logging, and run receipts. No daemon required.

## Quick start

Building requires Go 1.23 or later. The TOML library `github.com/pelletier/go-toml/v2` is the only third-party dependency and is compiled into the binary. Go is not required at runtime. Scheduled runs require an available systemd user manager or a launchd GUI domain.

```sh
git clone https://github.com/ivolejon/jobctl.git
cd jobctl
CGO_ENABLED=0 go install ./cmd/jobctl
```

Make sure Go's install directory (usually `$(go env GOPATH)/bin`) is on your `PATH`. From the repository root, try the `demo` job in the included `jobs.toml`:

```sh
jobctl validate
jobctl list
jobctl run demo --json
jobctl status demo
jobctl logs demo
jobctl logs demo --path
jobctl install demo --print
jobctl version
```

The demo writes `.jobctl-demo.txt` next to the config file. To activate its schedule, run `jobctl install demo`; remove it with `jobctl uninstall demo`. Keep the binary at a stable path when scheduling jobs.

`list` and `status` also support `--json`. Use `run --dry-run` to inspect the command, working directory, environment variable names, timeout, and locks without running the job or writing state. A run does not wait for locks by default; to wait, use `jobctl run --wait demo --wait-timeout 5m`.

## Configuration

`jobctl` looks for the config file in this order: `--config PATH`, `$JOBCTL_CONFIG`, `./jobs.toml`, then `$XDG_CONFIG_HOME/jobctl/jobs.toml` (default: `~/.config/jobctl/jobs.toml`). Pass `--config` and `--state-dir` before or after the subcommand. Subcommand flags can go on either side of the job name. If a job name starts with `-`, put flags first and use `--` before the name. Relative `cwd` and `env_file` paths are resolved from the config file's directory. `~` and `~/…` expand to the current user's home directory.

```toml
[jobs.nightly-gate]
description = "Nightly test run"
command = "scripts/nightly-gate.sh"
cwd = "."
schedule = "daily 02:00"
timeout = "3h"
kill_grace = "30s"
lock = ["heavy"]
retention_days = 30
env = { CI = "1" }
env_file = "~/.secrets"
nice = 10
io_idle = true

[jobs.web-unit]
command = "npm test"
cwd = "apps/web"
schedule = "every 30m"
timeout = "10m"
```

- Job and lock names must match `[a-z0-9-]+`. `command` is required. `cwd` must be an existing directory and defaults to the config file's directory.
- `timeout` and `kill_grace` are positive Go durations and default to `1h` and `30s`. `lock` accepts a name or a list, and defaults to the job name. Use `lock = []` to disable locking. Multiple locks are taken in sorted order. With `--wait`, the default is `30m` total across all locks.
- `env_file` is parsed as text, never as shell code. Use one `KEY=VALUE` per line; blank lines and lines beginning with `#` after optional leading whitespace are ignored. The value after the first `=` is taken literally. Precedence is inherited environment, then `env_file`, then `env`, with later values winning. `validate` also checks environment files.
- `enabled` defaults to `true`. Disabled jobs can still run manually, but `install` without names skips them and runs with `--trigger schedule` are blocked. Uninstall existing units explicitly; changing the TOML does not remove them.
- `validate` reports the file and line for each validation error, including unknown fields. Invalid TOML syntax or conflicting keys may stop decoding at the first error. Fix it and run `validate` again.

Only run **trusted** configurations. Do not put secrets in TOML or in the command itself: receipts record the command verbatim. Keep sensitive environment values in an access-restricted file outside version control. `jobctl` does not print those values, but `output.log` captures the job's stdout and stderr verbatim, so the job itself must not print secrets.

## Scheduling and installation

| Schedule | Example | Meaning |
| --- | --- | --- |
| `daily HH:MM` | `daily 02:00` | Every day |
| `weekly DAY[,DAY...] HH:MM` | `weekly mon,thu 02:00` | On the specified weekdays (`mon`–`sun`, no duplicates) |
| `every N[s/m/h]` | `every 30m` | A positive integer interval in seconds, minutes, or hours |

Calendar schedules use the machine's local time zone. `status` shows an estimate, not the scheduler's actual next run. For interval jobs, the estimate is based on the latest recorded start (including manual runs), or on the current time if there have been no runs. Login sessions, sleep, overlapping runs, and daylight saving time can affect actual run times.

`install --print` prints scheduler files to stdout without creating files or invoking the scheduler. An actual installation writes each file to the scheduler's directory and keeps a copy under `generated/` in the state directory. The installed files pin absolute paths to the binary, config, and state directory, and pass `--trigger schedule`. Reinstall if any of those paths change. Use `--force` to overwrite unmarked files; `uninstall` never removes unmarked files.

Without job names, `install` and `uninstall` use the jobs in the current config. If an enabled job has no schedule, explicitly name the jobs you want to install. `uninstall NAME` does not read the config, so it can remove the units of a job that was already removed from `jobs.toml`. A scheduler error can occur after files have been written; check the diagnostics and scheduler state before retrying.

- **Linux:** `~/.config/systemd/user/jobctl-<name>.{service,timer}`. User units normally run only while the user's session is active. To run without logging in, configure `loginctl enable-linger <user>` yourself. Calendar timers use `Persistent=true` and can catch up once when reactivated, rather than replaying every missed occurrence. Interval timers use `OnActiveSec` for the first run and `OnUnitActiveSec` thereafter, without `Persistent`. The service sets `TimeoutStartSec=infinity` so `jobctl` controls the timeout. `ExecStart=:` disables systemd environment variable expansion in its arguments. Installation rejects working directories containing line breaks or ending in whitespace or a backslash because `WorkingDirectory=` cannot represent them reliably.
- **macOS:** `~/Library/LaunchAgents/com.jobctl.<name>.plist`, in the user's `gui/<uid>` domain. Uninstall targets `gui/<uid>/com.jobctl.<name>` with `launchctl bootout`. launchd does not replay missed runs after a reboot. A calendar occurrence missed during sleep may run on wake. `StartInterval` uses seconds and does not catch up after a reboot.
- `nice` is passed to the scheduler, subject to OS limits. `io_idle` applies only to systemd. Neither changes the priority of a manual run.

## Runs and state

The state directory is selected by `--state-dir PATH`, `$JOBCTL_STATE_DIR`, or `$XDG_STATE_HOME/jobctl` (default: `~/.local/state/jobctl`). Use a private directory. `locks/` contains persistent flock files; do not delete them. Each `runs/<job>/<run-id>/` directory contains `output.log` (`0600`) and the atomically written `meta.json`. The `latest` symlink points to the newest run; if the link is missing or stale, `jobctl` checks the run directories directly.

After a run, `jobctl` removes older completed runs for the same job according to `retention_days` (default: 30 days). A value of `0` keeps the current run but may remove earlier completed runs immediately. Runs left marked as `running` after a crash or power loss are not cleaned up automatically.

Jobs run under `/bin/sh -c` in a separate process group. On timeout, `jobctl` sends `SIGTERM`. If `jobctl` receives `SIGINT` or `SIGTERM`, it forwards that signal. After `kill_grace`, any remaining group members receive `SIGKILL`. Processes that leave the group are not covered. The run receipt records the outcome (`passed`, `failed`, `timed_out`, `interrupted`, or `start_failed`) and the child's actual exit code or signal. A run in progress is shown as `running`. There is no receipt if locks could not be acquired.

`jobctl run` returns the job's own exit code, even when it overlaps with one of the codes below. Check the receipt's `status` to tell them apart. A job that dies from a signal on its own returns `128 + signal number` with status `failed`.

| Exit code | Meaning |
| ---: | --- |
| `0` | Success |
| `64` | Invalid command usage |
| `70` | Internal error |
| `78` | Configuration error |
| `124` | Job timed out |
| `125` | Shell process could not start |
| `126` | Lock was busy or the wait timed out |
| `130` / `143` | Interrupted by `SIGINT` / `SIGTERM` |

Diagnostics and the default `run` summary go to stderr. JSON, log output, and `install --print` go to stdout.

## Verification

```sh
export CGO_ENABLED=0
go vet ./...
go test ./...
go build ./...
for os in linux darwin; do
  for arch in amd64 arm64; do
    GOOS=$os GOARCH=$arch go build -o "/tmp/jobctl-$os-$arch" ./cmd/jobctl
  done
done
```

CI runs on Linux and macOS with Go 1.23. Integration tests use temporary directories and stubs for `systemctl` and `launchctl`; both scheduler formats are checked against golden files on each platform. Darwin builds use macOS system libraries, but no cgo or externally packaged libraries.

## License

[MIT](LICENSE).
