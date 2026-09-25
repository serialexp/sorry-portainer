#!/usr/bin/env bash
# Experiment 2: create -> init -> write into the container's own tmpfs through
# /proc/PID/root -> start. No Podman secret store, no OCI config, no disk.
source "$(dirname "$0")/lib.sh"
C=sorry-secret-inject
cleanup() { podman rm -f -t0 $C >/dev/null 2>&1; rm -rf "$EXP"; }
trap cleanup EXIT
cleanup; mkdir -p "$EXP"

echo "== create with tmpfs /run/secrets; the app reads the file at startup"
podman create --name $C --restart no --tmpfs /run/secrets:rw,size=1m,mode=0700 "$IMG" \
  sh -c 'echo "app saw: $(head -c 16 /run/secrets/token 2>&1)"; sleep 600' >/dev/null || exit 1

echo "== init (namespaces and mounts exist, app not started)"
podman init $C >/dev/null || exit 1
PID=$(podman inspect --format '{{.State.Pid}}' $C)
echo "  state=$(podman inspect --format '{{.State.Status}}' $C) pid=$PID"

echo "== inject as the agent user"
( umask 077; printf '%s' "$CANARY" > "/proc/$PID/root/run/secrets/token" ) && echo "  write ok"

echo "== start"
podman start $C >/dev/null; sleep 1
podman logs $C | sed 's/^/  /'
where
echo "  inspect hits: $(podman inspect $C | grep -c "$CANARY")"

echo "== restart: the tmpfs is new and empty"
podman restart -t0 $C >/dev/null; sleep 1
podman logs --since 2s $C | sed 's/^/  /'
