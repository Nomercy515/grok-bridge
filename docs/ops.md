# Operations: user service, self-check, remote reset

## Why the Bridge runs as your user (not root)

Incident (2026-10): opening a Build session from the phone failed with

    session/load: session/load: Path not found. ({"code":"FS_NOT_FOUND",...})

The hub ran as a **root** system service. It listed sessions from the user's
`~/.grok`, but the auto-started `grok agent serve` inherited `HOME=/root` and
used `/root/.grok`, which had no login (`grok models` → "You are not
authenticated") and none of the sessions. The agent could not find the session
the hub asked it to load → FS_NOT_FOUND.

A second bug made it worse: the agent was spawned with the context of the chat
request that triggered it, so it died when that turn ended. It is now started
detached and tracked so `/api/admin/reset` can stop and relaunch it.

Rule: nothing user-specific is hardcoded in the repo. The unit uses `%h`; the
clone path is resolved by `scripts/install-service.sh` from its own location.

## Install / update

```bash
cd <path-to-your-clone>
scripts/install-service.sh            # run as your normal user, NOT sudo
```

What it does:

1. Refuses to run as root.
2. Refuses if an old root `grok-bridge.service` / `grok-bridge-endpoint.service`
   system unit is still enabled (see migration below) or if `data/`, `certs/`,
   `bin/` contain files you don't own.
3. `sudo loginctl enable-linger $USER` (the only privileged step) so your user
   manager — and the Bridge — start at boot without a login. Skip with `--no-linger`.
4. Builds `bin/grok-bridge` if missing.
5. Renders `deploy/grok-bridge.user.service` (`@REPO@` → clone path) to
   `~/.config/systemd/user/grok-bridge.service` (previous copy kept as `.bak-<stamp>`).
6. `systemctl --user daemon-reload`, `enable`, `restart`, prints status.

Unit highlights (`deploy/grok-bridge.user.service`):

| Line | Why |
|------|-----|
| `Environment=GROK_HOME=%h/.grok`, `GROK_BRIDGE_GROK_HOME=%h/.grok` | pin the Grok home to your home |
| `Environment=PATH=%h/.grok/bin:%h/.local/bin:…` | find `grok` (and `go`) |
| `EnvironmentFile=-@REPO@/data/grok-bridge.env` | per-clone overrides + secrets (gitignored) |
| `ExecStartPre=-…/refresh-endpoint.sh --wait-secs 90 --no-regen-certs` | replaces the old root endpoint oneshot; does not regenerate certs each boot |
| `ExecStartPre=…/selfcheck.sh` | one-line checks in the journal |
| `Restart=always`, `RestartSec=3`, `WantedBy=default.target` | auto-recover, start with the user manager |

Ports 4020 (hub) and 2419 (agent) are > 1024, so no privileges are needed.
Logs: `journalctl --user -u grok-bridge -f` (or `sudo journalctl _SYSTEMD_USER_UNIT=grok-bridge.service`).

### WSL notes

* `/etc/wsl.conf` must have `[boot]` `systemd=true`.
* With linger on, `user@<uid>.service` starts when the WSL distro boots, so the
  Bridge starts too. WSL itself only boots when something starts it (opening a
  terminal, `wsl.exe`, or a Windows scheduled task such as
  `wsl.exe -d <distro> --exec /bin/true` at logon). Nothing user-specific needs
  to go into `/etc/wsl.conf`.

## Self-check

`scripts/selfcheck.sh` (ExecStartPre) and the hub at startup log lines like

    selfcheck ok run_user: user=<you> HOME=<your-home> grok_home=<your-home>/.grok
    selfcheck ok grok_binary: <your-home>/.grok/bin/grok
    selfcheck ok grok_auth: logged in
    selfcheck ok sessions_dir: <your-home>/.grok/sessions
    selfcheck ok agent_port: 127.0.0.1:2419 free

Only a missing `grok` binary fails the start.
`journalctl --user -u grok-bridge | grep selfcheck`

| FAIL | Fix |
|------|-----|
| `grok_auth` not logged in | `grok login` (or `grok login --device-auth`) as yourself |
| `agent_port` held by another user | a stale root agent: `sudo fuser -k 2419/tcp`, then reset |
| `run_user` root | uninstall the root unit; `scripts/install-service.sh` |
| `sessions_dir` missing | set `GROK_BRIDGE_GROK_HOME` in `data/grok-bridge.env` |

## Remote reset (phone)

Options (cog) → **Reset bridge (Grok agent)** → confirm. Same as

```bash
curl -sk -X POST -H "Authorization: Bearer $TOKEN" https://<your-tailnet-host>:4020/api/admin/reset
```

Response: `{"ok":true,"agentPid":1234,"stoppedPid":1200,"health":{"hub":true,"agent":true},"checks":[…]}`.
It stops the agent this hub auto-started (or a stray `grok agent serve` on the
port owned by the same user), relaunches it, reconnects, and re-runs the
self-check. **Restart Bridge hub** restarts the hub process itself.

## Migrating from the old root system service

```bash
STAMP=$(date +%Y%m%d-%H%M%S)
for u in grok-bridge.service grok-bridge-endpoint.service; do
  sudo cp /etc/systemd/system/$u /etc/systemd/system/$u.bak-$STAMP
  sudo systemctl disable --now $u
done
sudo systemctl daemon-reload
sudo chown -R "$USER": data certs bin        # files the root service created
scripts/install-service.sh
```

Secrets that only lived in `/etc/grok-bridge.env` (root-only) must be copied
into `data/grok-bridge.env` (mode 600) — the user service can't read `/etc`.

## Rollback

User service → previous user unit:

```bash
cp ~/.config/systemd/user/grok-bridge.service.bak-<stamp> ~/.config/systemd/user/grok-bridge.service
systemctl --user daemon-reload && systemctl --user restart grok-bridge
```

User service → old root system unit:

```bash
scripts/uninstall-service.sh
for u in grok-bridge.service grok-bridge-endpoint.service; do
  sudo cp /etc/systemd/system/$u.bak-<stamp> /etc/systemd/system/$u
done
sudo systemctl daemon-reload && sudo systemctl enable --now grok-bridge-endpoint grok-bridge
```

Code: `git checkout <previous-branch> && go build -o bin/grok-bridge ./cmd/grok-bridge`.
