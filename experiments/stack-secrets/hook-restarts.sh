#!/usr/bin/env bash
# Experiment 3: an OCI hook injects the secret on every start, including
# Podman's own restart-policy restarts.
#   STAGE=createRuntime | prestart | createContainer
#   HOOKMODE=conf (containers.conf hooks_dir) | flag (podman --hooks-dir)
#   NO_TMPFS=1 omits the tmpfs: the hook must refuse and the start must fail
source "$(dirname "$0")/lib.sh"
STAGE=${STAGE:-createRuntime}
HOOKMODE=${HOOKMODE:-conf}
HOOKS=$EXP/hooks.d
C=sorry-secret-hook
cleanup() { "${PODMAN[@]}" rm -f -t0 $C >/dev/null 2>&1; rm -rf "$EXP"; }
PODMAN=(podman)
trap cleanup EXIT
install_hook "$HOOKS" "$STAGE"
( umask 077; printf '%s' "$CANARY" > "$EXP/value" )
if [[ $HOOKMODE == conf ]]; then use_hooks_conf "$HOOKS"; else PODMAN=(podman --hooks-dir "$HOOKS"); fi
"${PODMAN[@]}" rm -f -t0 $C >/dev/null 2>&1

TMPFS=(--tmpfs /run/secrets:rw,size=1m,mode=0700)
[[ ${NO_TMPFS:-} == 1 ]] && TMPFS=()
echo "### STAGE=$STAGE HOOKMODE=$HOOKMODE NO_TMPFS=${NO_TMPFS:-0} runtime=$(podman info --format '{{.Host.OCIRuntime.Name}}')"
"${PODMAN[@]}" run -d --name $C --restart on-failure:3 --annotation io.sorry-portainer.secrets=token \
  "${TMPFS[@]}" "$IMG" \
  sh -c 'echo "app saw: $(head -c 16 /run/secrets/token 2>&1)"; sleep 2; exit 1' >/dev/null
echo "== after the first start and three on-failure restarts"
sleep 9
"${PODMAN[@]}" logs $C | sed 's/^/  /'
echo "  restart count: $("${PODMAN[@]}" inspect --format '{{.RestartCount}}' $C)"
echo "  hook runs: $(grep -c '^stage=' "$EXP/hook.log" 2>/dev/null || echo 0)"
where
echo "  inspect hits: $("${PODMAN[@]}" inspect $C | grep -c "$CANARY")"
