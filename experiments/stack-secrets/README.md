# Stack-secret experiments

Reproducible checks behind `docs/design/stack-secrets.md`. Each script uses a
random throwaway canary, never a real secret, and searches Podman's disk and
tmpfs storage for it. Run as the rootless user under test (ideally the
dedicated agent user) on each supported host:

```bash
MODE=default   ./podman-secrets.sh    # Podman secrets: value lands on disk
MODE=transient ./podman-secrets.sh    # --transient-store does not change that
./init-inject.sh                      # create -> init -> inject -> start
STAGE=createRuntime ./hook-restarts.sh
STAGE=prestart      ./hook-restarts.sh
HOOKMODE=flag       ./hook-restarts.sh # --hooks-dir: restarts skip the hook
NO_TMPFS=1          ./hook-restarts.sh # no tmpfs: hook refuses, start fails
./compose-hook.sh                     # through podman-compose
```

Pass means: the app line shows the canary on every start, and the
`disk (graphroot)` and `inspect` results are empty.

State lives in `$XDG_RUNTIME_DIR/sorry-secret-exp` (tmpfs) and is removed on
exit. The scripts create containers named `sorry-secret-*` / `sorrysecretexp_*`
and remove them on exit.
