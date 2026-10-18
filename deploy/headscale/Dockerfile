# Headscale with lan's config baked in, for hosts that run a single image and
# only allow env vars (no config volume, no exec). Build from this folder:
#   docker build --platform linux/amd64 -t <image> deploy/headscale
# Env: HEADSCALE_SERVER_URL (required), LAN_HEADSCALE_API_KEY (seeded at boot,
# see entrypoint.sh), optional HEADSCALE_DERP_SERVER_IPV4.
# Mount a volume at /var/lib/headscale to keep state across restarts.
FROM headscale/headscale:0.29.5 AS headscale

FROM alpine:3.22
RUN apk add --no-cache ca-certificates curl sqlite apache2-utils \
 && mkdir -p /var/lib/headscale /var/run/headscale /etc/headscale \
 && chmod 1777 /var/lib/headscale /var/run/headscale
COPY --from=headscale /ko-app/headscale /usr/local/bin/headscale
COPY config.yaml /etc/headscale/config.yaml
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080 3478/udp
ENTRYPOINT ["/entrypoint.sh"]
