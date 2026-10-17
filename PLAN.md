# PLAN.md

## Current plan

**As of:** 2026-10-17
**Driving objective:** O1

### Strategy

v1 is built and passes the local e2e against a real Headscale. Next, deploy the stack and test with real Clerk login on two machines on different networks.

### Steps

1. Create the Clerk OAuth application (public client, PKCE, redirect `http://127.0.0.1:53682/callback`)
2. Deploy `deploy/` to a VPS (DNS, `.env`, Headscale API key)
3. Two-machine test across NATs; confirm `direct`, and the relay fallback when UDP is blocked

### Open questions

- Is Clerk's OAuth app flow OK with a public PKCE client and a loopback redirect? This is untested against real Clerk.

### Risks / unknowns

- The Clerk userinfo field names (`sub`, `username`, `email`) are assumed from Clerk's docs
- `docker build` of lan-server hasn't been run here (no Docker daemon); `docker compose config` validates

---

## Recently superseded plans

- *2026-10-17: none yet*
