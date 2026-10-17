# PROJECT.md

## Name

lan

## One-line description

A CLI that gives developers private networks where they can share a local port or a read-only folder with teammates, peer-to-peer.

## Why this exists

Developers need to show each other local work (dev servers, APIs, build output) without exposing it publicly or setting up a VPN. lan does it with app-level WireGuard (tsnet), a self-hosted control plane (Headscale) and the Clerk identity we already have. It's standalone for now and is meant to be embedded elsewhere later.

## In scope

- Clerk login, create network, invite codes, join, kick
- `lan up`: roster (direct vs relay), port shares, read-only folder shares, connect
- Self-hosted server stack (Caddy + Headscale + lan-server) via Docker Compose

## Out of scope

- UI of any kind, mobile, two-way sync, chat
- Daemon / background service (restart `lan up` to change shares)

## Stack & conventions

Go 1.27, two modules (`cli/` → `lan`, `server/` → `lan-server`), tailscale.com v1.104.1 (tsnet), Headscale v0.29.5, golang.org/x/oauth2. Spec: DESIGN.md.

## Stakeholders

- **Owner:** suraj
- **Other peers:** see `.xo/peers.json`

## How to run / build / test

```
cd cli && go install .                      # lan
cd server && go build .                     # lan-server
./e2e/run.sh                                # full local e2e (real Headscale), 17 checks
cd deploy && docker compose up -d           # server stack, see deploy/README.md
```
