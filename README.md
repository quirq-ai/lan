# lan

Private networks for developers. Create a network, invite teammates, and share a **local port** or a **folder (read-only)** with them peer-to-peer. Nothing is exposed to the internet, and every connection is identity-checked.

```bash
alice$ lan up --port web=3000 --dir assets=./build
bob$   lan up --connect alice/web=8080 --connect alice/assets=8081
bob$   curl localhost:8080          # → Alice's localhost:3000
```

Built on [Headscale](https://github.com/juanfont/headscale) (control plane), [tsnet](https://pkg.go.dev/tailscale.com/tsnet) (WireGuard inside the app: no root, no system VPN) and Clerk (identity). Design: [DESIGN.md](DESIGN.md).

## Layout (each folder deploys separately)

| Folder | What | Ships as |
|---|---|---|
| `cli/` | `lan`, the developer CLI | a binary on each developer's machine |
| `server/` | `lan-server`: networks, invites, Clerk auth, the Headscale API | a container (`server/Dockerfile`) |
| `deploy/` | Docker Compose: Caddy + Headscale + lan-server | one VPS, see [deploy/README.md](deploy/README.md) |
| `e2e/` | full local test against a real Headscale | `./e2e/run.sh` |

## Install the CLI

```bash
cd cli && go install .        # → $(go env GOPATH)/bin/lan   (Go ≥ 1.27)
```

## Use

```bash
lan login --server https://lan.example.com    # opens the browser (Clerk)

# owner
lan create design-team                        # prints: lan join lan_xxxx…
lan invite                                    # another code (24h, single use)
lan up --port web=3000 --dir assets=./build   # go online and share; Ctrl-C to leave
lan kick bob

# teammate
lan join lan_xxxx… [--as bob]
lan up --connect alice/web=8080 --connect alice/assets=8081
open http://localhost:8081                    # browse Alice's ./build
```

`lan up` prints who's online and whether each connection is `direct` or going through the relay. If you're in several networks, add `--net <name>`. `lan networks` lists them.

## Test

```bash
./e2e/run.sh
```

This starts Headscale v0.29.5, lan-server (with dev auth) and three nodes (alice, bob, carol) on this machine, then checks port sharing, the direct path, folder sharing (read-only, no `../` or symlink escapes), isolation between networks, kick, and invite reuse. Last run: 17/17 passing.

## Dev auth

`LAN_DEV_AUTH=1` on lan-server accepts `dev-<name>` tokens (`lan login --token dev-alice`), so you can test without Clerk. Never set it in production.
