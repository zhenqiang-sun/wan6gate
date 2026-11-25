English | [简体中文](README_ZH.md)

# wan6gate

A small self-hosted gateway that exposes your home services through your router's public IPv6 address. Built for homes where IPv4 is trapped behind carrier-grade NAT (CGNAT) but the ISP hands out a real public IPv6 prefix.

It ships as a single Go binary that runs on the router and provides:

- **TCP/UDP port forwarding** — `[router-ipv6]:N` → `<lan-ip>:M`
- **Authenticated SOCKS5 proxy** (RFC 1928 + 1929) — turn your router into a clean exit node
- **IPv6 DDNS for Aliyun DNS** — your domain follows the WAN GUA assigned after PPPoE dialing
- **Feishu webhook notifications** — daemon start, DDNS changes, DDNS errors
  (Currently supports Aliyun DNS and Feishu webhook notifications. PRs welcome for Cloudflare DNS, Telegram, Discord, etc.)
- **Generic scheduled tasks** (new in v0.3.0) — run shell commands or HTTP requests on a cron schedule, with failure alerts to Feishu
- **In-memory metrics + hourly persisted history** — kept for 90 days, with simple SVG charts
- **SOCKS5 brute-force protection** — 10 failures within 5 minutes → the IP is banned for 30 minutes
- **Single-page LAN-only web UI** — no frontend framework, and HTTPS is deliberately not enabled
- **One-click client configuration export** — for sing-box / Clash / Surge / generic URI

The whole project is written for one specific deployment scenario: an aarch64 Xiaomi router running MiWiFi firmware (based on OpenWrt-18.06). Persistence relies on uci + fw3 + busybox cron, which are common on OpenWrt branches but are not guaranteed to behave the same on other firmware. Port with care; see [docs/architecture.md](docs/architecture.md) for what you need to replace.

## Network Environment Pre-check

Before deploying, confirm that your broadband satisfies two prerequisites — otherwise the core value of wan6gate (a public IPv6 exit) is not realized:

1. **Check whether your broadband sits behind CGNAT.**
   Visit <https://ip-address.site> and look at your current exit IP. If it falls in `100.64.0.0/10`, `10.0.0.0/8`, `172.16.0.0/12`, or `192.168.0.0/16`, your IPv4 is shared with other subscribers and cannot be reached directly from the public Internet via port forwarding — this is exactly the problem wan6gate solves: use a public IPv6 address to bypass CGNAT.
   To understand the mechanism and its impact, read <https://ip-address.site/guides/what-is-cgnat-and-why-it-matters>.

2. **Check that you have a genuine public IPv6 prefix (GUA).**
   On the same <https://ip-address.site> page, your IPv6 address should be a global unicast address starting with `2xxx:` — not `fe80::` (link-local) and not an address in `fd00::/8` (ULA private range).
   If no public IPv6 shows up, contact your ISP to enable IPv6 and make sure the router's WAN interface has been assigned an IPv6 prefix.

> 💡 Tip: The same page reports both your IPv4 and your IPv6 exit addresses, so a single visit covers both checks.

## Prerequisites

wan6gate is deployed to the router over SSH, so you must first unlock SSH access using **XMiR-Patcher**:

> **XMiR-Patcher** — a firmware patcher for Xiaomi routers
> Repository: https://github.com/openwrt-xiaomi/xmir-patcher

Steps:
1. Download XMiR-Patcher
2. Run `run.bat` (Windows) or `run.sh` (Linux/macOS)
3. Select your router model and enable SSH access

**Important**: SSH must be enabled before you can build and deploy.

## Quick Start

```bash
# 1. Cross-compile the aarch64 Linux binary
./scripts/build.sh                 # → build/wan6gate (~7 MB, statically linked)

# 2. Push and install it to the router (environment variables required)
export ROUTER_HOST=<router-lan-ip>          # e.g. 192.168.31.1
export ROUTER_PASS='your-router-root-password'
./scripts/deploy.sh

# 3. Open http://<router-lan-ip>:18080 to set the admin password and add mapping rules
```

## Build Prerequisites (build host)

- Go ≥ 1.22 (development environment uses 1.26)
- `expect` for the password-mode SSH helper scripts (bundled with macOS; on Linux: `apt install expect`, etc.)

No CGO, no `node_modules`, no frontend toolchain — the UI is hand-written HTML / CSS / JS embedded into the binary via `//go:embed`.

## Target Platform Prerequisites (router)

- aarch64 / arm64 Linux (typical Xiaomi router models such as BE7000 and AX9000 are aarch64 ARMv8)
- Dropbear SSH (firmware default; the build helper scripts cope with legacy `ssh-rsa`-only host keys)
- BusyBox `crond` reading `/etc/crontabs/`
- `uci` + `fw3` (firewall configuration tooling — used to persist port rules)
- A writable `/data/` partition (binary + configuration + logs)
- BusyBox `flock` (used by the watchdog)

The binary has zero external runtime dependencies; it shells out to `/sbin/uci` and `/etc/init.d/firewall reload` to apply firewall changes.

## Build

```bash
./scripts/build.sh
```

Equivalent to:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" \
  -o build/wan6gate ./cmd/wan6gate
```

## Deploy

```bash
export ROUTER_HOST=<router-lan-ip>        # your router's LAN IP, e.g. 192.168.31.1
export ROUTER_PASS='your-root-password'    # never commit this
./scripts/deploy.sh                        # build + scp + install
./scripts/deploy.sh --no-build             # skip the rebuild
./scripts/deploy.sh --no-install           # push the binary only
```

What `deploy.sh` does behind the scenes:

1. Cross-compiles (unless `--no-build` is given).
2. `scp`s the binary to `/data/router-ops/bin/wan6gate` via the `scripts/_ssh-helper.exp` wrapper script (this works around Dropbear's outdated algorithm set).
3. Stops any running `wan6gate daemon`.
4. Runs `wan6gate install` on the router, setting up:
   - the `/data/router-ops/{bin,wan6gate/logs}` directories
   - `/etc/crontabs/patches/wan6gate_watchdog.sh` (restarts the daemon within ≤ 60 seconds after any crash or startup)
   - the cron line that runs the watchdog every minute
   - a fw3 rule named `wan6gate_ui_lockdown_18080` that explicitly DROPs WAN traffic to the UI port
5. Restarts cron.

## Runtime Layout on the Router

```
/data/router-ops/
├── bin/wan6gate                       binary (ubifs, persistent)
└── wan6gate/
    ├── config.json                    mode 0600; mappings + socks5 + ddns + notification config
    ├── config.json.v3.bak             automatic migration backup, kept from first boot
    ├── wan6gate.lock                  singleton flock file
    ├── wan6gate.pid                   PID of the running daemon
    ├── logs/wan6gate.log              size-rotated 1 MB × 3
    └── metrics/YYYY-MM.jsonl          hourly snapshots, retained for 90 days

/etc/crontabs/patches/wan6gate_watchdog.sh
/etc/config/firewall                   uci sections wan6gate_<port>_<proto> + wan6gate_ui_lockdown_*
```

`/data` is ubifs (survives reboots). `/etc` is ramfs, but `/etc/config/` and `/etc/crontabs/` are bind-mounted onto persistent storage by this firmware — that is why the design works without ever touching the squashfs rootfs.

## Subcommands

```
wan6gate daemon                 run in the foreground; invoked by the cron watchdog
wan6gate install                idempotent setup
wan6gate uninstall              clean removal (keeps the config by default)
wan6gate uninstall --purge      also removes the config and the binary
wan6gate version
```

`daemon` accepts `--config <path>` and `--bind host:port` overrides, which makes local testing on a non-router host possible.

## Local Testing (no router required)

```bash
go build -o build/wan6gate-mac ./cmd/wan6gate
WAN6GATE_DIR=/tmp/wan6gate-test \
  ./build/wan6gate-mac daemon --bind 127.0.0.1:18080
```

Open `http://127.0.0.1:18080`. When those binaries are missing, firewall calls (`uci`, `firewall reload`) warn and continue, so the UI / forwarder / SOCKS5 still work end to end (against simulated LAN targets).

## Verify the SOCKS5 Exit IP

Once the SOCKS5 proxy is configured and you have exported a client configuration, you can verify that the exit IP has actually switched to your home router's public IPv6:

- **Via the browser**: point your browser at the exported client configuration so it routes through the SOCKS5 proxy, then visit <https://ip-address.site>. The IPv6 address shown there should be your router's WAN-side public IPv6.
- **Via the command line**:

  ```bash
  curl -x socks5h://<user>:<pass>@<router-ipv6>:<port> https://ip-address.site
  ```

  where `<router-ipv6>` is the router's public IPv6 address, `<port>` is the SOCKS5 listening port, and `<user>` / `<pass>` are the credentials you set in the UI.

If the result still shows your ISP's shared exit IP rather than your router's IPv6, check that: the router's WAN interface really obtained a GUA prefix, the DDNS record has been updated to the newest address, and the SOCKS5 listener is bound to the correct address.

## Dependencies

```
golang.org/x/crypto/bcrypt    UI admin password hashing
```

Everything else (HTTP server, JSON, Aliyun DNS RPC signing, SOCKS5 protocol, atomic metrics, SVG chart rendering) is the standard library plus hand-written code.

## Out of Scope

- HTTPS for the UI (LAN-only by design)
- Multi-user SOCKS5 (one credential per listener; open more listeners if you need more)
- IPv4 DDNS (IPv4 is assumed to be behind CGNAT)
- SOCKS5 UDP ASSOCIATE (TCP CONNECT covers the described use cases)
- Routers without uci + fw3 + busybox-cron (port required)

## Security Best Practices

- **Never map the web UI port (default `18080`) directly to the public Internet.** That port serves plain HTTP by design — no HTTPS, no extra authentication layer — so exposing it is effectively opening your router's management plane to the whole Internet. `wan6gate install` writes an fw3 rule named `wan6gate_ui_lockdown_18080` that drops WAN traffic destined for that port; keep that rule in place.
- **Use an encrypted channel for remote management.** Connect from the LAN directly where possible; if you must go outside, use SSH local port forwarding (`ssh -L 18080:<router-lan-ip>:18080 root@<router>`), or first hop through this project's authenticated SOCKS5 proxy and reach the LAN web UI through it.
- **Keep management credentials and SOCKS5 passwords out of the repository.** The deploy scripts read the password from the `ROUTER_HOST` / `ROUTER_PASS` environment variables, and `.gitignore` already excludes local key files such as `.env`, `*.pem`, and `*.key`.
- **Treat the SOCKS5 listener as a public-facing exposure as well.** Brute-force protection is built in (10 failures within 5 minutes → 30-minute ban), but you should still restrict source addresses via the firewall and use a long random password.
- **Back up `/data` before upgrading.** A router's `/data` partition is usually only a few dozen MB, and having the old and new binaries present at the same time can fill it up; `scripts/deploy.sh` uses a delete-then-move atomic swap to avoid that problem.

## Common Gotchas

- Dropbear on Xiaomi firmware usually advertises only `ssh-rsa` host keys, which modern OpenSSH refuses to connect to. The helper scripts pass options such as `HostKeyAlgorithms=+ssh-rsa`.
- BusyBox `crond` does not support `@reboot`; we use `*/1 * * * *` plus a flock-protected watchdog instead. Do not try to use `@reboot`.
- `/etc/init.d/dropbear` is recreated from squashfs on every boot; Xiaomi's built-in `ssh_patch.sh` restarts dropbear on each boot, which picks up our `/etc/config/dropbear` `Interface=lan` setting (LAN-only listening).
- After the LAN subnet changes, a fresh install binds the UI to `0.0.0.0:18080` by default; existing configurations keep their original IP. Edit it in the UI if you need a specific address.

## Status

Tested in a single home production environment. Stable across reboots, PPPoE re-dials, and LAN renumbering. See [CHANGELOG.md](CHANGELOG.md) for the scope of each release and [docs/architecture.md](docs/architecture.md) for the current design.

## Open-Source Scope and Disclaimer

- This project is provided **as-is under the MIT license**, with no warranties of any kind; the author accepts no responsibility for device damage, data loss, or security consequences arising from its use.
- The project has **only been validated on specific Xiaomi (MiWiFi / OpenWrt-18.06-based `aarch64`) firmware**. Other brands differ widely in the behavior and paths of `uci` / `fw3` / BusyBox `crond`.
- **No cross-brand router compatibility is promised, and firmware flashing (reflashing) is not supported.** This project neither contains nor guides you through any firmware-flashing procedure; the risk of bricking your device through self-flashing is entirely yours to bear.
- Porting to other firmware means replacing the persistence layer (`uci` + `fw3` + cron watchdog) yourself — see [docs/architecture.md](docs/architecture.md) for the relevant interfaces.

## License

MIT — see [LICENSE](LICENSE).
