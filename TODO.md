# Follow-up

- Found during relay hardening (2026-09-24), not yet fixed:
  - Both HTTP listeners in `cmd/sorry-portainer-server/main.go` use `http.ListenAndServe`/`http.Serve` with no `ReadHeaderTimeout`/`IdleTimeout`, so slow clients can hold connections open indefinitely.
  - Server shutdown is not graceful: no signal handling, sessions are dropped by process exit.

- Expose actual Compose/Podman stack runtime status instead of the current `saved` metadata status. The stack list explicitly labels this limitation.
- Verify `podman-compose` successful exit implies the intended workload was created and started; the installed provider has previously printed pull errors while returning exit code 0. Do not equate operation success with running containers until this check exists.

- Found while building stack secrets (2026-09-24):
  - **Needs Bart's decision:** stopping/restarting an agent service kills every container it started. Podman leaves `conmon` in the systemd unit's cgroup when `INVOCATION_ID` is set (`libpod/oci_conmon_linux.go`). Options in `docs/design/stack-secrets.md`.
  - **Needs Bart's decision:** after an agent restart, the reconnect push restarts every running container that uses secrets, because the agent's empty vault sees every value as changed. Options in `docs/design/stack-secrets.md`.
  - podman-compose 1.0.6 `down` leaves `<project>_default` networks behind. Older test runs left several `sorry-podman-test-*` and `sorry-local-a-podman-smoke-*` networks on Bart's machine (not removed; not mine). The secrets live test removes its own.
  - Production agent layout: `oci_hooks_dir` defaults to `~/.local/share/sorry-portainer/oci-hooks` while `state_dir` is set separately; decide where each lives for the dedicated user.
  - Verify the hook with crun, and on Ubuntu 26.04 / Podman 5.x under the dedicated agent user.
  - UI prop drilling (Rule 9): `index.tsx` passes `state`/`setState` into `Dashboard`, and `StackList`/`StackEditor` take data props. The new secrets UI uses a Solid store (`src/secrets-store.ts`); moving the dashboard state to stores the same way is recommended before more UI work.
  - UI: the sidebar container count and host page don't refresh after a stack start; "Save and start" on the edit page saves a new revision even when nothing changed; the secrets panel's delivery line doesn't refresh after an unlock push.
  - Stack deletion doesn't exist yet; when added it must delete the stack's secrets on the master and make the agent forget them.
  - Backups of the master's sealed secret store (`<state_dir>/secrets`) are undecided.
