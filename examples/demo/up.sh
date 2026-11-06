#!/usr/bin/env bash
# Start a fresh Home Assistant in Docker, onboard it, and print the provider's environment.
#
#   eval "$(./up.sh)"        # HA_VERSION=2026.6.4 ./up.sh for another version
#
# ./down.sh removes the container again.
set -euo pipefail
cd "$(dirname "$0")"

name=ha-demo
version=${HA_VERSION:-2026.11.0}
port=${HA_PORT:-8123}

docker create --name "$name" -p "$port:8123" \
  "ghcr.io/home-assistant/home-assistant:$version" >/dev/null
docker cp config/. "$name:/config/"
docker start "$name" >/dev/null

echo "Waiting for Home Assistant $version on port $port ..." >&2
until curl -sf "http://localhost:$port/api/onboarding" >/dev/null; do sleep 2; done

token=$(docker exec -i "$name" python3 - < onboard.py)
echo "Log in at http://localhost:$port as demo / demo-password." >&2
echo "export HOMEASSISTANT_URL=http://localhost:$port"
echo "export HOMEASSISTANT_TOKEN=$token"
