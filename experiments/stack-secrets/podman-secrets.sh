#!/usr/bin/env bash
# Experiment 1: where does rootless Podman put a secret whose driver keeps the
# value only in tmpfs?  MODE=default | transient (podman --transient-store)
source "$(dirname "$0")/lib.sh"
MODE=${MODE:-default}
PODMAN=(podman)
[[ $MODE == transient ]] && PODMAN=(podman --transient-store)
P=sorry-secret-exp
WRITE="SORRYWRITE$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
STORE=$EXP/store

cleanup() {
  "${PODMAN[@]}" rm -f -t0 $P-mount $P-env >/dev/null 2>&1
  "${PODMAN[@]}" secret rm $P >/dev/null 2>&1
  rm -rf "$EXP"
}
trap cleanup EXIT
cleanup
mkdir -p "$STORE"
echo "### MODE=$MODE"

echo "== create secret (file driver whose store lives in tmpfs)"
printf '%s' "$CANARY" | "${PODMAN[@]}" secret create --driver file --driver-opts path="$STORE" $P - >/dev/null || exit 1
where

echo "== run a mount-secret container and an env-secret container"
"${PODMAN[@]}" run -d --name $P-mount --secret $P,type=mount "$IMG" sleep 600 >/dev/null
"${PODMAN[@]}" run -d --name $P-env --secret $P,type=env,target=APP_TOKEN "$IMG" sleep 600 >/dev/null
echo "  inside mount container: $("${PODMAN[@]}" exec $P-mount cat /run/secrets/$P | head -c 16)..."
echo "  inside env container:   $("${PODMAN[@]}" exec $P-env sh -c 'printf %s "$APP_TOKEN"' | head -c 16)..."
echo "  /run/secrets mounts:"
"${PODMAN[@]}" exec $P-mount grep ' /run/secrets' /proc/mounts | sed 's/^/    /'
where
echo "  inspect reveals value: env=$("${PODMAN[@]}" inspect $P-env | grep -c "$CANARY") mount=$("${PODMAN[@]}" inspect $P-mount | grep -c "$CANARY")"

echo "== where does the container's writable layer live?"
"${PODMAN[@]}" exec $P-mount sh -c "echo $WRITE > /written.txt && sync"
where "$WRITE"

echo "== driver store gone (agent memory lost), then restart"
mkdir -p "$STORE.bak" && mv "$STORE"/* "$STORE.bak"/
"${PODMAN[@]}" restart -t0 $P-mount $P-env 2>&1 | sed 's/^/  /'
echo "  mount after restart: $("${PODMAN[@]}" exec $P-mount cat /run/secrets/$P 2>&1 | head -c 16)"
echo "  env after restart:   $("${PODMAN[@]}" exec $P-env sh -c 'printf %s "$APP_TOKEN"' 2>&1 | head -c 40)"

echo "== remove containers, then look again"
"${PODMAN[@]}" rm -f -t0 $P-mount $P-env >/dev/null
where
# A secret whose store vanished cannot be removed until the store is back.
mv "$STORE.bak"/* "$STORE"/ && rmdir "$STORE.bak"
