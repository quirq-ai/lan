# PROGRESS.md

> Running narrative of what's actually been done. **Append-only** — never edit prior entries. One paragraph per session at close. Read by every agent at boot (last ~30 lines only).

## Format

```
## YYYY-MM-DD — [outcome] one-line headline
agent: <model id>

3–6 sentences: what was attempted, what shipped, what's blocked, what's next.
Be honest about partial work.
```

`[outcome]` ∈ `shipped | progress | blocked | pivoted | cleanup | research`.

---


## 2026-10-17 — [shipped] lan v1: CLI, server, deploy stack, e2e green
agent: claude-opus-5-5

Built the MVP from DESIGN.md: `cli/` (lan: login/create/invite/join/networks/up/kick; tsnet node with port shares, read-only folder shares via os.Root, connect forwarders, roster), `server/` (lan-server: networks/invites/members in state.json, Clerk userinfo auth + dev auth, Headscale REST client, per-network ACL policy), `deploy/` (Compose: Caddy + Headscale 0.29.5 with embedded DERP + lan-server) and `e2e/run.sh`. The e2e runs a real Headscale and three nodes and passes 17/17: port share with a direct path, folder share (404 on ../ and symlink escapes, 405 on writes), isolation between networks, kick (access lost in about 1s), and invite reuse rejected. Not yet verified: real Clerk login, `docker build`, and a real VPS deploy. Next: create the Clerk OAuth app and deploy.
