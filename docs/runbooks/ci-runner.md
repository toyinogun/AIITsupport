# Runbook: the CI runner on the Raspberry Pi

CI (`.github/workflows/ci.yml`) runs on two self-hosted GitHub Actions runners on a Raspberry Pi 5 (8 GB, Ubuntu 24.04, linux/arm64), reached over Tailscale. Both carry the label `rpi01`.

## Which runner a job uses

Every job has the same `runs-on` expression:

```yaml
runs-on: ${{ github.event.pull_request.head.repo.fork && 'ubuntu-latest' || 'rpi01' }}
```

- A push to `main`, or a pull request from a branch in this repo, runs on the Pi.
- A pull request from a fork runs on `ubuntu-latest`. The repo is public, and the Pi sits on the tailnet, so code from outside the repo never runs there.

The Go cache key includes `runner.arch`, so the Pi's arm64 builds and GitHub's x64 builds never share a cache entry. The `test` job maps Postgres to a random host port, so two runs at once on the Pi do not collide.

## Layout on the Pi

| Path | What |
|---|---|
| `~/actions-runner-1`, `~/actions-runner-2` | the two runners (`rpi01-1`, `rpi01-2`), each with its own `_work` |
| `~/.cache/actions-runner/` | the downloaded runner tarball |

| `~/.config/systemd/user/actions-runner@.service` | the systemd user unit; `actions-runner@1` and `@2` are the two instances |

Both run as the login user, who is in the `docker` group (service containers, testcontainers, buildx, and QEMU need Docker). They are systemd **user** services, not system ones, so no step needs sudo. Lingering (`loginctl enable-linger`) keeps them running with nobody logged in and starts them at boot.

## Install or reinstall

0. Install a C compiler: `sudo apt-get install -y gcc libc6-dev`. The `test` job runs `go test -race`, the race detector needs cgo, and Go turns cgo off when it finds no C compiler (`go: -race requires cgo`). This is the only step that needs sudo.
1. Download the arm64 runner tarball from `github.com/actions/runner/releases`, check it against the SHA-256 in the release notes, and unpack it into each runner directory.
2. Register each one with a fresh token (it lasts an hour):

   ```bash
   gh api -X POST repos/toyinogun/AIITsupport/actions/runners/registration-token --jq .token
   ```

   then on the Pi, in each runner directory:

   ```bash
   ./config.sh --unattended --replace --url https://github.com/toyinogun/AIITsupport --token <token> --name rpi01-<n> --labels rpi01 --work _work
   ```

3. Turn on lingering, then write the user unit (a template that runs `%h/actions-runner-%i/run.sh` with `Restart=always`, `KillMode=process`, `WantedBy=default.target`) and start both:

   ```bash
   loginctl enable-linger "$USER"
   systemctl --user daemon-reload && systemctl --user enable --now actions-runner@1 actions-runner@2
   ```

## Check on it

- GitHub: Settings, Actions, Runners lists `rpi01-1` and `rpi01-2` as Idle or Active. From a laptop: `gh api repos/toyinogun/AIITsupport/actions/runners --jq '.runners[] | "\(.name) \(.status)"'`.
- On the Pi: `systemctl --user status 'actions-runner@*'`, or `journalctl --user -u 'actions-runner@*' -f`.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Jobs sit in "Waiting for a runner" | both runners offline (Pi off, Tailscale down, or the service stopped) | `systemctl --user restart 'actions-runner@*'`; GitHub drops a runner that is offline for 14 days, then re-register it |
| Disk fills up | Docker images and build cache from the image jobs | `docker system prune -af --filter until=168h` |
| `samba-image` is slow | the amd64 half builds under QEMU on ARM | expected on the first run; the `gha` layer cache keeps later runs short |
| A runner updates itself and stops | the runner auto-updates; `Restart=always` normally brings it back | `systemctl --user restart actions-runner@<n>` |

## Remove

`systemctl --user disable --now actions-runner@1 actions-runner@2`, then in each runner directory `./config.sh remove --token <token>` with a removal token from `gh api -X POST repos/toyinogun/AIITsupport/actions/runners/remove-token --jq .token`. Then set every `runs-on` back to `ubuntu-latest`.
