# Shared setup for the stack-secret experiments. Source, don't run.
# Uses a random throwaway canary, never a real secret.
set -u
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
IMG=${IMG:-quay.io/libpod/busybox:latest}
EXP=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/sorry-secret-exp # tmpfs: stands in for agent memory
GRAPH=$(podman info --format '{{.Store.GraphRoot}}')
RUN=$(podman info --format '{{.Store.RunRoot}}')
CANARY="SORRYCANARY$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
mkdir -p "$EXP"
chmod 700 "$EXP"

# where [VALUE] lists every file under Podman's disk and tmpfs storage that
# holds VALUE (default: the canary).
where() {
  local value=${1:-$CANARY}
  echo "  disk (graphroot $GRAPH):"
  podman unshare grep -rlF "$value" "$GRAPH" 2>/dev/null | sed 's/^/    /'
  echo "  tmpfs (runroot $RUN):"
  podman unshare grep -rlF "$value" "$RUN" 2>/dev/null | grep -v "^$EXP" | sed 's/^/    /'
  echo "  /var/tmp, podman config and cache:"
  grep -rlF "$value" /var/tmp ~/.config/containers ~/.cache/containers 2>/dev/null | sed 's/^/    /'
}

# install_hook DIR STAGE writes a hook JSON that runs hook.sh at STAGE for
# containers carrying the io.sorry-portainer.secrets annotation.
install_hook() {
  local dir=$1 stage=$2
  mkdir -p "$dir"
  chmod +x "$HERE/hook.sh"
  printf '{"version":"1.0.0","hook":{"path":"%s","args":["hook.sh","%s","%s","%s"]},"when":{"annotations":{"^io\\\\.sorry-portainer\\\\.secrets$":".+"}},"stages":["%s"]}\n' \
    "$HERE/hook.sh" "$stage" "$EXP/value" "$EXP/hook.log" "$stage" > "$dir/sorry.json"
}

# use_hooks_conf DIR points Podman (and its restart-policy cleanup process) at
# DIR through a containers.conf override, standing in for the agent user's
# ~/.config/containers/containers.conf.
use_hooks_conf() {
  printf '[engine]\nhooks_dir = ["%s"]\n' "$1" > "$EXP/containers.conf"
  export CONTAINERS_CONF_OVERRIDE=$EXP/containers.conf
}
