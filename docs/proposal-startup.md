# Proposal: portable foreground startup

Status: suggestion only. No code in this change. Implement only if you agree.

Date: 2026-11-11. Written from a real Windows install at `%USERPROFILE%\.pi-gateway` (port 18080, manual startup).

Japanese summary: `gateway up` はランチャを読まず、ポート 8080 に別プロセスを立てて 10 秒待って失敗する。常駐プロセスは `gateway serve` のフォアグラウンド 1 本にし、設定はバイナリが `~/.pi-gateway` から読む。ログイン起動は OS ごとの登録（systemd user / LaunchAgent / Windows のログオン登録）が同じコマンドを実行する。シェルの `up.cmd` / `up.sh` は環境変数を配る役をやめる。

## Problem

The installed gateway is started by `up.cmd`, which sets `PORT`, `BIND`, `CONFIG_FILE`, `SECRETS_FILE`, and `AGE_IDENTITY_FILE`, then runs `gateway.exe up`. The wizard tells the user to run `gateway up` or "the launcher" as if those were the same action (`internal/wizard/interactive.go`, `PrintSummary`).

`gateway up` does not read the runtime directory. With those variables unset it probes `http://127.0.0.1:8080/healthz` (`cmd/gateway/service_cmd.go`, `resolveGatewayURL`). Nothing is listening there, so it spawns `gateway.exe serve` with an empty environment. `serve` exits at once because `AGE_IDENTITY_FILE` / `AGE_IDENTITY` / `AGE_IDENTITY_CMD` are missing (`cmd/gateway/main.go`, `resolveIdentitySource`). The parent waits 10 seconds and reports failure, pointing at `gateway.log` in the current directory.

Observed on this machine on 2026-11-11:

- The real server was already healthy on `127.0.0.1:18080` (log: `%USERPROFILE%\.pi-gateway\gateway.log`).
- Bare `gateway up` exited 1 with `gatewayが10秒以内にREADYになりませんでした。ログ gateway.log を確認してください`.
- The repo-local `gateway.log` recorded `環境変数 AGE_IDENTITY_FILE / AGE_IDENTITY / AGE_IDENTITY_CMD のいずれかを設定してください`.

The menu, doctor, and key commands already resolve `~/.pi-gateway` (`setup.ResolveDir`). Doctor already chooses a port as `PORT` env, then `setup.json`, then `PORT=` parsed from the launcher, then `8080` (`internal/ops/doctor.go`, `doctorPort`). `gateway init` already fills the same variables in-process before calling `serviceUp` (`cmd/gateway/init_cmd.go`, `startGatewayNow`). Startup is the path that does not share that lookup.

A second, separate defect: `gateway up` detaches the child with `DETACHED_PROCESS` only (`cmd/gateway/spawn_windows.go`). On Windows that child dies with the parent job. systemd and launchd do not want a self-daemonized child anyway.

## Desired behavior

One foreground `gateway serve` process per install. The binary loads the install. The OS keeps the process alive. The same command line works on Windows, Linux, and macOS.

| OS | Supervisor | Registered command |
|---|---|---|
| Linux | systemd `--user`, `Type=simple`, restart on failure | `$HOME/.pi-gateway/bin/gateway serve` |
| macOS | LaunchAgent, `RunAtLoad` and `KeepAlive` | `$HOME/.pi-gateway/bin/gateway serve` |
| Windows | logon registration that does not open a console, and restarts if you want that | `%USERPROFILE%\.pi-gateway\bin\gateway.exe serve` |

`gateway down` and `GET /healthz` stay HTTP calls. They do not need a shell or a process tree.

### How `serve` finds the install

When the environment variables are unset and `~/.pi-gateway` exists (or `--runtime-dir` is passed):

- config, secrets, and identity come from that directory's layout (`setup.Layout`)
- port and bind come from `setup.json`, then from `PORT=` / `BIND=` in the existing launcher, then from today's defaults (`8080`, `127.0.0.1`)
- the server log defaults to `<runtime>/gateway.log`, not a `gateway.log` in the current directory

Explicit environment variables still win, so tests, `SECRETS_DECRYPT_CMD`, and hand-built layouts keep working. A checkout with no runtime directory still defaults to port `8080` and `config/models.yaml`.

If the runtime directory exists but the identity cannot be loaded, `serve` exits immediately and names `gateway` (the setup menu) as the next step. It does not wait 10 seconds.

### What the user runs

- First setup and later changes: bare `gateway` (the menu), unchanged.
- Login autostart: the OS runs `gateway serve`. No `up.cmd` / `up.sh` in that command line.
- Manual start in a terminal: `gateway serve` stays in the foreground until the user stops it. That is the contract systemd and launchd already have.
- `gateway up` remains an idempotent check for scripts that already call it. It resolves the runtime directory, probes that port, and exits 0 when `/healthz` is up. When nothing is up, it does not spawn a second detached server. It prints the `serve` command, or starts the OS unit / agent / logon task if one is registered. It must not be the thing that forks `serve`.

Wizard copy in `PrintSummary` should name that one action. It should stop offering bare `gateway up` as equal to the launcher, and it should stop saying "your alias" now that Pi ids are the real model name.

## Windows, without a different application

HKCU `Run` only starts a process at logon. It does not restart it, and a console-subsystem `gateway.exe` opens a window. Keep the binary a console program so the menu still has a console. Hide the window in the Windows registration only (Task Scheduler logon trigger is enough if you also want restart). Linux and macOS do not need that special case.

User systemd and a LaunchAgent both run at login, which matches the dormant login-resident goal in `docs/design-distribution.md` §6. They do not run before login unless the user turns on lingering or a system daemon. That is acceptable.

## Compatibility

- Existing `up.cmd` / `up.sh` can remain on disk for one release so old shortcuts keep working. New registrations must not depend on them. Stop writing environment assignments into new launchers.
- `setup.json` without a port, and manual installs that only have a launcher, keep working through the launcher parse doctor already does.
- External unit files that exec a bare `gateway` need the migration note already required by `docs/design-distribution.md` §1.1: `ExecStart` is `gateway serve`. `GATEWAY_SERVE=1` stays as the legacy escape hatch.
- Do not change the "no runtime dir and no `PORT`" default of `8080`. The wizard's 18080 lives in the install, not in the server default.

## Files this would touch

- `cmd/gateway/main.go` — runtime-dir fallback inside `serve` before the identity check.
- `cmd/gateway/service_cmd.go` — `gateway up` probes the resolved port and stops detaching `serve`.
- `cmd/gateway/spawn_windows.go` and `spawn_unix.go` — no longer used by `up` if detach is removed.
- `internal/ops/doctor.go` — share `doctorPort` / `launcherEnv` instead of a third copy of the lookup.
- `internal/wizard/interactive.go` — completion text.
- `internal/setup/launcher.go` — registration writers per OS, replacing env-bearing `up.cmd` / `up.sh` for new installs.
- `README.md`, `CHANGELOG.md`, and `docs/design-distribution.md` §1.1 and §6.

Autostart registration itself is still the later batch called out in §6. This proposal only fixes the process contract those registrations should target.

## Done when

- On an install created by the wizard, `gateway serve` with a clean environment listens on the install port, opens `<runtime>/gateway.log`, and serves `/healthz`.
- `gateway up` with a clean environment reports the running install and exits 0. It does not start a process on port 8080.
- A missing identity fails at once, with no 10 second wait and no log file in the caller's directory.
- The same `serve` command line is what a systemd user unit, a LaunchAgent, and the Windows logon entry run.
- A dev checkout with no `~/.pi-gateway` still starts on port 8080 when `PORT` is unset.
- Existing env-based launches (`PORT`, `CONFIG_FILE`, `SECRETS_FILE`, `AGE_IDENTITY_FILE`, `SECRETS_DECRYPT_CMD`) behave as they do today.
