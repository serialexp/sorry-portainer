# Follow-up

- Found during relay hardening (2026-09-24), not yet fixed:
  - `certificateIdentity` (`internal/server/control.go`) matches the URI scheme with `strings.HasPrefix(uri.Scheme, "sorry-host")`, so `sorry-hostile://x` is also accepted. Only certificates from our own CA reach it, but it should be an exact match.
  - Both HTTP listeners in `cmd/sorry-portainer-server/main.go` use `http.ListenAndServe`/`http.Serve` with no `ReadHeaderTimeout`/`IdleTimeout`, so slow clients can hold connections open indefinitely.
  - Server shutdown is not graceful: no signal handling, sessions are dropped by process exit.

- Expose actual Compose/Podman stack runtime status instead of the current `saved` metadata status. The stack list explicitly labels this limitation.
- Verify `podman-compose` successful exit implies the intended workload was created and started; the installed provider has previously printed pull errors while returning exit code 0. Do not equate operation success with running containers until this check exists.
