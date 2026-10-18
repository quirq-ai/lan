#!/bin/sh
# Starts Headscale and, if LAN_HEADSCALE_API_KEY is set, seeds that API key into
# its database, so lan-server can use the same key without anyone running
# `headscale apikeys create` inside the container.
#
#   HEADSCALE_SERVER_URL   public https URL of this Headscale (required)
#   LAN_HEADSCALE_API_KEY  hskey-api-<12-char prefix>-<64-char secret> (optional)
set -eu

# State goes in /var/lib/headscale when this user can write there (the image
# makes it world-writable; a mounted volume may not be), otherwise /tmp.
DATA=/var/lib/headscale
if ! (touch "$DATA/.w" && rm -f "$DATA/.w") 2>/dev/null; then
	DATA=/tmp/headscale
	echo "warning: /var/lib/headscale not writable; using $DATA (state is lost on restart)" >&2
fi
mkdir -p "$DATA"
export HEADSCALE_DATABASE_SQLITE_PATH="$DATA/db.sqlite"
export HEADSCALE_NOISE_PRIVATE_KEY_PATH="$DATA/noise_private.key"
export HEADSCALE_DERP_SERVER_PRIVATE_KEY_PATH="$DATA/derp_server_private.key"
export HEADSCALE_UNIX_SOCKET="$DATA/headscale.sock"

headscale serve &
pid=$!
trap 'kill -TERM $pid 2>/dev/null' TERM INT

if [ -n "${LAN_HEADSCALE_API_KEY:-}" ]; then
	rest=${LAN_HEADSCALE_API_KEY#hskey-api-}
	prefix=$(printf %s "$rest" | cut -c1-12)
	secret=$(printf %s "$rest" | cut -c14-)
	if [ "$rest" = "$LAN_HEADSCALE_API_KEY" ] || [ ${#prefix} -ne 12 ] || [ ${#secret} -ne 64 ]; then
		echo "LAN_HEADSCALE_API_KEY is not hskey-api-<12>-<64>; not seeding" >&2
	else
		# Wait until Headscale has created and migrated the database. Stop
		# waiting (and let `wait` below exit the container) if it died.
		i=0
		until curl -fs http://127.0.0.1:8080/health >/dev/null 2>&1; do
			kill -0 $pid 2>/dev/null || break
			i=$((i + 1)); [ $i -gt 60 ] && { echo "headscale did not become healthy" >&2; break; }
			sleep 1
		done
		if kill -0 $pid 2>/dev/null; then
			# htpasswd emits $2y$, the same algorithm Go's bcrypt reads as $2a$.
			hash=$(htpasswd -nbBC 10 x "$secret" | cut -d: -f2 | sed 's/^\$2y\$/$2a$/')
			sqlite3 "$HEADSCALE_DATABASE_SQLITE_PATH" "INSERT INTO api_keys (prefix, hash, created_at)
				SELECT '$prefix', CAST('$hash' AS BLOB), datetime('now')
				WHERE NOT EXISTS (SELECT 1 FROM api_keys WHERE prefix = '$prefix');"
			echo "seeded API key with prefix $prefix"
		fi
	fi
fi

# Exit with Headscale's status so the platform restarts a dead container
# instead of keeping a shell alive with nothing on :8080.
wait $pid
