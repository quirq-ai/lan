# Deploy the lan server stack

One VPS with Docker. It runs Caddy (TLS) → Headscale (control plane + embedded DERP relay) and lan-server.

## 1. DNS and firewall

- Two A records pointing at the VPS: `HS_DOMAIN` (e.g. `hs.example.com`) and `LAN_DOMAIN` (e.g. `lan.example.com`)
- Open **tcp/80**, **tcp/443** and **udp/3478** (STUN)

## 2. Clerk

Dashboard → **Configure → OAuth applications** → add an application ([docs](https://clerk.com/docs/advanced-usage/clerk-idp)):

- Public client (PKCE), scopes `profile email`
- Redirect URI: `http://127.0.0.1:53682/callback` (the CLI's loopback listener)
- Note the **client ID** and your **Frontend API URL** (the issuer)

## 3. Configure and boot

```bash
cd deploy
cp .env.example .env            # fill HS_DOMAIN, LAN_DOMAIN, PUBLIC_IPV4, CLERK_*
docker compose up -d caddy headscale

# lan-server needs a Headscale API key
docker compose exec headscale headscale apikeys create --expiration 999d
# → paste into .env as HEADSCALE_API_KEY, then:
docker compose up -d --build lan-server
```

## 4. Check

```bash
curl https://$LAN_DOMAIN/healthz          # ok
curl https://$HS_DOMAIN/health            # {"status":"pass"}
docker compose logs lan-server            # no "initial policy sync failed"
```

Then on a laptop: `lan login --server https://$LAN_DOMAIN`.

## Notes

- State lives in Docker volumes: `headscale-data` (Headscale DB + keys) and `lan-data` (`state.json`). Back up both.
- `headscale/config.yaml` sets `policy.mode: database`, which is required because lan-server writes the ACL policy through the API.
- Only the embedded DERP relay is configured (`derp.urls: []`), so no third-party relays are used.
