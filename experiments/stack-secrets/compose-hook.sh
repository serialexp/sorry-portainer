#!/usr/bin/env bash
# Experiment 4: the hook through podman-compose, then a manual stop/start.
source "$(dirname "$0")/lib.sh"
HOOKS=$EXP/hooks.d
PROJECT=sorrysecretexp
cleanup() {
  (cd "$HERE/compose" && podman-compose -p $PROJECT down -t 0 >/dev/null 2>&1)
  rm -rf "$EXP"
}
trap cleanup EXIT
install_hook "$HOOKS" createRuntime
use_hooks_conf "$HOOKS"
( umask 077; printf '%s' "$CANARY" > "$EXP/value" )

cd "$HERE/compose" || exit 1
podman-compose -p $PROJECT up -d >/dev/null 2>&1 || { echo "compose up failed"; exit 1; }
sleep 1
echo "== first start";               podman logs ${PROJECT}_app_1 | sed 's/^/  /'
podman stop -t0 ${PROJECT}_app_1 >/dev/null; podman start ${PROJECT}_app_1 >/dev/null; sleep 1
echo "== after manual stop/start";   podman logs --since 2s ${PROJECT}_app_1 | sed 's/^/  /'
where
echo "  inspect hits: $(podman inspect ${PROJECT}_app_1 | grep -c "$CANARY")"
