# lan — MVP spec

A tiny CLI that lets developers create a private **network**, invite teammates, and share a **local port** or a **folder (read-only)** with them peer-to-peer. Nothing is exposed publicly. Build the lightest thing that works: two Go binaries, one JSON file of state, no UI.

```bash
alice$ lan up --port web=3000 --dir assets=./build
bob$   lan up --connect alice/web=8080 --connect alice/assets=8081
bob$   curl localhost:8080          # → Alice's localhost:3000
bob$   open http://localhost:8081   # → browse Alice's ./build (read-only)
```

## Components

| Piece | What it is | Reference |
|---|---|---|
| **Headscale** | Control plane + embedded DERP relay, on one VPS. Used as-is. | [repo](https://github.com/juanfont/headscale) · [DERP](https://headscale.net/stable/ref/derp/) · [policy/ACLs](https://headscale.net/stable/ref/acls/) · [REST API](https://headscale.net/stable/ref/api/) |
| **`lan-server`** | Small Go HTTP server next to Headscale. Checks Clerk identity, owns networks/members/invites, and is the only thing that calls Headscale. | — |
| **`lan`** | The CLI. `lan up` embeds a Tailscale node via `tsnet` (no root, no system VPN). | [tsnet docs](https://pkg.go.dev/tailscale.com/tsnet) · [tsnet guide](https://tailscale.com/kb/1244/tsnet) |
| **Clerk** | Identity. Set up as an OAuth provider; the CLI logs in with PKCE. | [Clerk as IdP](https://clerk.com/docs/advanced-usage/clerk-idp) · [`x/oauth2`](https://pkg.go.dev/golang.org/x/oauth2) (PKCE helpers) |

```
 VPS:  headscale (:443, STUN udp/3478)   lan-server (:8443)   state.json
           ▲  netmap                         ▲  REST + Clerk token
           │                                 │
 alice: lan up (tsnet node) ◄──WireGuard──► bob: lan up (tsnet node)
```

## CLI (complete list)

```
lan login [--server URL] [--token T]   # Clerk OAuth PKCE, loopback redirect → saves token
lan create <name> [--as handle]        # creates + joins as owner → prints invite code
lan invite [--net n]                   # new invite code (24h, single use; owner)
lan join <code> [--as handle]
lan networks                           # list networks you're in
lan up [--net n] [--port name=localport]... [--dir name=path]... [--connect peer/share=localport]... [-v]
lan kick <handle> [--net n]            # owner only
```

`lan up` runs in the foreground until Ctrl-C. It prints the roster on start and whenever it changes:

```
network design-team · you are alice (100.64.0.1)
shares: web → :3000   assets → ./build (read-only)
peers:
  bob          online  direct
  carol        online  relay(lan)
```

There's no daemon or socket. To change shares or connections, restart `up`.

## lan-server API

All calls send `Authorization: Bearer <clerk access token>`. `lan-server` resolves the caller by calling Clerk's `GET https://<clerk-frontend-api>/oauth/userinfo` (no JWKS handling needed).

| Endpoint | Does |
|---|---|
| `GET /healthz`, `GET /config` | Unauthenticated. `/config` gives the CLI `{clerkIssuer, clientID, devAuth}` for login |
| `POST /networks {name, handle}` | Create a network, admit the caller as owner; returns the join response + `code` |
| `POST /networks/{id}/invites` | Owner only; returns `{code}` |
| `POST /join {code, handle}` | Validate the code → Headscale user → policy → pre-auth key; returns `{network, handle, authKey, controlURL, owner}`. A failed join gives the invite back |
| `POST /networks/{id}/kick {handle}` | Owner only; delete nodes → remove member → policy |

**State** is one `state.json` behind a mutex: `networks{id,name,owner}`, `members{network,clerkID,handle,hsUser,hsUserID}`, `invites{sha256(code),network,expires,used}`.

**Headscale calls** (v0.29.5, Bearer API key; see `/swagger` on your server):

```
POST   /api/v1/user          {"name":"<handle>-<netid>"}              → user.id
POST   /api/v1/preauthkey    {"user":"<id>","reusable":false,
                              "expiration":"<now+5m>"}                   → key
PUT    /api/v1/policy        {"policy":"<HuJSON string>"}   (needs policy.mode: database)
GET    /api/v1/node          → filter by user; DELETE /api/v1/node/{id}  (kick)
```

**Policy** is regenerated from `state.json` on every join or kick, and pushed once at boot. There's one group per network, so networks can't see each other:

```jsonc
{
  "groups": { "group:s91x": ["alice-s91x@", "bob-s91x@"] },
  "acls":   [{ "action": "accept", "src": ["group:s91x"], "dst": ["group:s91x:*"] }]
}
```

## `lan up`: implementation sketch

```go
s := &tsnet.Server{
    Hostname:   cfg.Handle,                    // "alice"
    Dir:        "~/.lan/networks/<id>/tsnet",  // node state, persists identity
    ControlURL: cfg.ControlURL,                // https://hs.example.com
    AuthKey:    cfg.AuthKey,                   // only needed on first run
}
lc, _ := s.LocalClient()
```

1. **Manifest.** `s.Listen("tcp", ":7000")` serves `GET /shares` → `[{"name":"web","type":"port","port":7001}, ...]`.
2. **Port share.** `s.Listen("tcp", ":700N")`; for each connection, dial `127.0.0.1:<localport>` and `io.Copy` in both directions.
3. **Dir share.** `s.Listen("tcp", ":700N")` serves `http.FileServer(http.FS(root.FS()))` with `root, _ := os.OpenRoot(path)`. [`os.Root`](https://pkg.go.dev/os#Root) blocks `../` and symlink escapes. Only GET and HEAD are allowed.
4. **Auth on every inbound connection** (manifest and shares): `who, _ := lc.WhoIs(ctx, remoteAddr)`. Reject unless `who.UserProfile.LoginName` (minus anything after `@`) ends in `-<netid>`. This backs up the ACL.
5. **Connect.** Resolve `peer` → IP from `lc.Status()` (match `Peer[].HostName`; only same-network peers are visible). Fetch `http://<ip>:7000/shares`, listen on `127.0.0.1:<localport>`, and for each connection `s.Dial(ctx, "tcp", ip:port)` and `io.Copy` in both directions.
6. **Roster.** Poll `lc.Status()` every 5 s. A peer is `direct` if `CurAddr != ""`, otherwise it shows `relay(<Relay>)`.

**Local files** (root overridable with `LAN_HOME`): `~/.lan/config.json` (server URL + Clerk token) and `~/.lan/networks/<id>/net.json` (`id, name, handle, controlURL, authKey, owner`). The single-use `authKey` is cleared after the first successful `lan up`.

**Dev auth:** `LAN_DEV_AUTH=1` on lan-server accepts `dev-<name>` bearer tokens (`lan login --token dev-alice`). It's for the e2e test only.

## Repo layout

```
cli/        lan        main.go (commands) · config.go (files, API client) · login.go (Clerk PKCE) · up.go (tsnet node)
server/     lan-server main.go (API) · store.go (state.json) · headscale.go (client + policy) · auth.go (Clerk) · Dockerfile
deploy/     docker-compose.yml (caddy + headscale + lan-server) · Caddyfile · headscale/config.yaml · .env.example
e2e/        run.sh: real Headscale + lan-server + 3 nodes, checks the list below
```

## Done when (all checked by `e2e/run.sh`)

1. Alice and Bob are on **different networks**. Bob's `curl localhost:8080` returns Alice's `:3000`, and the roster shows `direct`.
2. Bob can browse and download from `alice/assets`. A `../` or symlink-escape request returns 404.
3. A member of another network can't see Alice in the roster and can't connect to her shares.
4. After `lan kick`, Bob loses access within 10 seconds.
5. A used or expired invite code is rejected.
