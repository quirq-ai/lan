#!/usr/bin/env bash
# End-to-end test: real Headscale + lan-server (dev auth) + three lan nodes on
# this machine. Checks the five "Done when" criteria from DESIGN.md.
#
#   ./e2e/run.sh          # needs Go ≥1.26 and internet (Headscale download, DERP map)
set -uo pipefail

HS_VERSION=0.29.5
ROOT=$(cd "$(dirname "$0")/.." && pwd)
E2E=$ROOT/e2e
RUN=$E2E/.run
BIN=$E2E/.bin
SERVER=http://127.0.0.1:18443

pass=0; fail=0
ok()   { echo "  ✔ $*"; pass=$((pass+1)); }
bad()  { echo "  ✘ $*"; fail=$((fail+1)); }
step() { echo; echo "── $*"; }

pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null; done; wait 2>/dev/null; }
trap cleanup EXIT

# wait_log FILE PATTERN SECONDS
wait_log() { for _ in $(seq 1 "$3"); do grep -qE "$2" "$1" 2>/dev/null && return 0; sleep 1; done; return 1; }

step "build"
rm -rf "$RUN" && mkdir -p "$RUN/hs" "$RUN/logs" "$BIN"
(cd "$ROOT/server" && go build -o "$BIN/lan-server" .) || exit 1
(cd "$ROOT/cli" && go build -o "$BIN/lan" .) || exit 1
if [ ! -x "$BIN/headscale" ]; then
  curl -fsSL -o "$BIN/headscale" \
    "https://github.com/juanfont/headscale/releases/download/v${HS_VERSION}/headscale_${HS_VERSION}_linux_amd64" || exit 1
  chmod +x "$BIN/headscale"
fi
echo "  built lan, lan-server; headscale $("$BIN/headscale" version | head -1)"

step "start headscale + lan-server"
sed "s#__DIR__#$RUN/hs#g" "$E2E/headscale.yaml" > "$RUN/hs/config.yaml"
HS="$BIN/headscale -c $RUN/hs/config.yaml"
$HS serve >"$RUN/logs/headscale.log" 2>&1 & pids+=($!)
for _ in $(seq 1 20); do curl -fs http://127.0.0.1:18080/health >/dev/null && break; sleep 0.5; done
APIKEY=$($HS apikeys create 2>/dev/null | tail -1)
[ -n "$APIKEY" ] || { echo "no api key"; cat "$RUN/logs/headscale.log"; exit 1; }

LAN_STATE=$RUN/state.json LAN_LISTEN=127.0.0.1:18443 HEADSCALE_URL=http://127.0.0.1:18080 \
HEADSCALE_API_KEY=$APIKEY LAN_CONTROL_URL=http://127.0.0.1:18080 LAN_DEV_AUTH=1 \
  "$BIN/lan-server" >"$RUN/logs/lan-server.log" 2>&1 & pids+=($!)
for _ in $(seq 1 20); do curl -fs $SERVER/healthz >/dev/null && break; sleep 0.5; done
curl -fs $SERVER/healthz >/dev/null && echo "  up" || { cat "$RUN/logs/lan-server.log"; exit 1; }

# lan as a given user: each user gets its own LAN_HOME
as() { local who=$1; shift; LAN_HOME=$RUN/$who LAN_SERVER=$SERVER "$BIN/lan" "$@"; }
for u in alice bob carol; do as $u login --token dev-$u >/dev/null; done

# Something for Alice to share: a dev server and a folder (with a symlink escape)
mkdir -p "$RUN/site" "$RUN/assets"
echo "hello from alice's dev server" > "$RUN/site/index.html"
echo "alice's build output" > "$RUN/assets/readme.txt"
ln -s /etc/hostname "$RUN/assets/escape"
python3 -m http.server 18000 --bind 127.0.0.1 --directory "$RUN/site" >/dev/null 2>&1 & pids+=($!)

step "create, join, up"
CODE=$(as alice create design-team | awk '/lan join/{print $3}')
[ -n "$CODE" ] && ok "alice created design-team, invite $CODE" || bad "create failed"
as bob join "$CODE" >/dev/null && ok "bob joined" || bad "bob join failed"

as alice up --port web=18000 --dir assets="$RUN/assets" >"$RUN/logs/alice.log" 2>&1 & pids+=($!)
wait_log "$RUN/logs/alice.log" '^shares:' 60 && ok "alice online and sharing" || bad "alice up: $(tail -3 $RUN/logs/alice.log)"
as bob up --connect alice/web=18081 --connect alice/assets=18082 >"$RUN/logs/bob.log" 2>&1 & pids+=($!)
wait_log "$RUN/logs/bob.log" 'connect: alice/assets' 60 && ok "bob online and connected" || bad "bob up: $(tail -3 $RUN/logs/bob.log)"

step "1. port share: bob → alice:3000-style dev server, direct path"
body=$(curl -fs --max-time 10 localhost:18081/)
[[ "$body" == *"hello from alice"* ]] && ok "curl localhost:18081 → \"$body\"" || bad "port share returned: '$body'"
wait_log "$RUN/logs/bob.log" 'alice +online  direct' 30 && ok "bob's roster shows alice direct" \
  || bad "no direct path: $(grep -A3 peers: $RUN/logs/bob.log | tail -3)"

step "2. folder share: read-only, no escapes"
body=$(curl -fs --max-time 10 localhost:18082/readme.txt)
[[ "$body" == "alice's build output" ]] && ok "download readme.txt" || bad "download returned: '$body'"
curl -fs --max-time 10 localhost:18082/ | grep -q readme.txt && ok "directory listing works" || bad "no listing"
code=$(curl -s -o /dev/null -w '%{http_code}' --path-as-is localhost:18082/../../etc/passwd)
[ "$code" = 404 ] && ok "../ traversal → 404" || bad "../ traversal → $code"
code=$(curl -s -o /dev/null -w '%{http_code}' localhost:18082/escape)
[ "$code" = 404 ] && ok "symlink escape → 404" || bad "symlink escape → $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data x localhost:18082/new.txt)
[ "$code" = 405 ] && ok "PUT → 405 (read-only)" || bad "PUT → $code"

step "3. isolation: carol (another network) can't see or reach alice"
as carol create other-team >/dev/null
as carol up --connect alice/web=18083 >"$RUN/logs/carol.log" 2>&1 & pids+=($!)
wait_log "$RUN/logs/carol.log" '^peers:' 60
grep -q 'no other members yet' "$RUN/logs/carol.log" && ok "carol's roster is empty" || bad "carol sees peers: $(cat $RUN/logs/carol.log)"
body=$(curl -s --max-time 5 localhost:18083/)
[ -z "$body" ] && ok "carol's connect to alice/web gets nothing" || bad "carol reached alice: '$body'"
# Even knowing Alice's IP, the ACL drops carol's packets (checked from alice's netmap side)
grep -q 'carol' "$RUN/logs/alice.log" && bad "alice sees carol in roster" || ok "alice never sees carol"

step "4. kick: bob loses access within 10s"
as alice kick bob >/dev/null && ok "alice kicked bob" || bad "kick failed"
lost=""
for i in $(seq 1 10); do
  sleep 1
  curl -fs --max-time 2 localhost:18081/ >/dev/null || { lost=$i; break; }
done
[ -n "$lost" ] && ok "bob lost access after ${lost}s" || bad "bob still has access after 10s"

step "5. invites: used code is rejected"
out=$(as carol join "$CODE" 2>&1)
[[ "$out" == *"invalid, used or expired"* ]] && ok "reusing bob's code → rejected" || bad "reuse: $out"

echo
echo "passed $pass, failed $fail   (logs: $RUN/logs)"
[ "$fail" -eq 0 ]
