# Project facts

> Distilled invariants of this project. Stack, conventions, structural rules. One claim per line. No narrative.

- Two Go modules: cli/ (module `lan`) and server/ (module `lan-server`); each deploys separately.
- Headscale must run with `policy.mode: database`; lan-server writes the ACL via PUT /api/v1/policy.
- One Headscale user per (Clerk user, network): name `<handle>-<netid>`; the policy groups them as `group:<netid>` with `user@` syntax.
- tsnet nodes see only same-network peers (Headscale filters the netmap by ACL), so the roster and peer lookup use lc.Status().
- Headscale config accepts HEADSCALE_* env overrides (e.g. HEADSCALE_SERVER_URL, HEADSCALE_DERP_SERVER_IPV4).
- Local Go toolchain: ~/.local/go (1.27.2); add it to PATH.
