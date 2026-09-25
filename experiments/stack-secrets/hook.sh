#!/bin/sh
# OCI hook stand-in for the agent's injector. Called by the OCI runtime with
# the container state on stdin.
#   args: hook.sh STAGE VALUE_FILE LOG_FILE
# Writes VALUE_FILE's content to /run/secrets/token inside the container, but
# only if /run/secrets is a tmpfs mount there (never the writable layer).
STAGE=$1
VALUE=$2
LOG=$3
STATE=$(cat)
PID=$(printf '%s' "$STATE" | sed -n 's/.*"pid":\([0-9]*\).*/\1/p')
BUNDLE=$(printf '%s' "$STATE" | sed -n 's/.*"bundle":"\([^"]*\)".*/\1/p')
ROOTFS=$(sed -n 's/.*"root":{"path":"\([^"]*\)".*/\1/p' "$BUNDLE/config.json")
echo "stage=$STAGE pid=$PID rootfs=$ROOTFS" >> "$LOG"
# Before pivot_root the container's mount namespace still has the host root, so
# its tmpfs sits under the rootfs path; after pivot it would be at /run/secrets.
for target in "$ROOTFS/run/secrets" /run/secrets; do
  if awk -v t="$target" '$5 == t && $0 ~ / - tmpfs / { found = 1 } END { exit !found }' "/proc/$PID/mountinfo" 2>/dev/null; then
    if (umask 077; cat "$VALUE" > "/proc/$PID/root$target/token") 2>>"$LOG"; then
      echo "  wrote $target/token" >> "$LOG"
      exit 0
    fi
  fi
done
echo "  refused: no tmpfs /run/secrets in the container's mount namespace" >> "$LOG"
exit 1
