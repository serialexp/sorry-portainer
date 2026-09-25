# Current task: stack secrets — implemented; two decisions open

## Stack-secret implementation (2026-09-24)

Wheat change: "Stack secrets: validation groundwork and master secret store" (`01a0d1bf…`). Checklist, deviations and open decisions: `docs/design/stack-secrets.md`.

- Master: `internal/secretstore` (Argon2id + AES-GCM, sealed file per value in `<state_dir>/secrets`), `internal/server/secrets.go` (routes, pusher, delivery status). Server starts locked; unlock in the UI.
- Relay: protocol v4 `secrets.sync` / `secrets.retain`; master pushes on change, unlock and agent connect.
- Agent: `internal/secrets` (vault, socket, `oci-hook`, verify, hook JSON), `internal/stacks/compose.go` + `secrets.go` (rewrite to `runtime-compose.yaml`, post-up check, rotation restarts), `cmd/sorry-portainer-agent` (`oci-hook`, `setup-hooks-conf [--print]`), `internal/agentsetup/hooks.go` (containers.conf.d drop-in).
- UI: `src/secrets-api.ts`, `src/secrets-store.ts` (Solid store), `src/secrets-ui.tsx`; wired into `index.tsx` and `stacks-ui.tsx`.
- Dev: `just install-dev-services` writes `.dev/state/containers-hooks.conf`; agent units set `CONTAINERS_CONF_OVERRIDE` to it. The dev secret store was initialized with a throwaway passphrase (told to Bart, not recorded here); delete `.dev/state/secrets` to reset it. Demo stack `secret-demo` on local-a is running.
- Tests: `GOFLAGS=-buildvcs=false go test -race ./...`; `node --test tests/*.mjs`; live: `SORRY_PORTAINER_TEST_PODMAN=1 GOFLAGS=-buildvcs=false go test -run TestRealPodman ./internal/stacks/` (passed 3× in a row).
- Found by the live test and fixed: `--annotation` values are CSV-parsed, so the mount list is `source:target:uid:gid:mode;…`.
- Open (need Bart): agent stop kills containers (`INVOCATION_ID`/conmon cgroup); agent restart restarts all secret containers. Both with options in the design doc and TODO.md.

## Stack-secret experiment (2026-09-24)

Bart's requirement: secrets not kept on agent disks; only in container memory or on the master.

- Results and design: `docs/design/stack-secrets.md`. Reproducible scripts: `experiments/stack-secrets/` (see its README).
- Podman native secrets are ruled out: any driver, the value is copied to `graphroot/overlay-containers/<id>/userdata/secrets/` (mount) or `userdata/config.json` (env). `--transient-store` does not help.
- Working mechanism: OCI `createRuntime` hook (agent binary) writes into the container's own `/run/secrets` tmpfs via `/proc/<pid>/root/<rootfs>/run/secrets`. Works on first start, manual start, podman-compose up, and Podman restart-policy restarts, but only when `hooks_dir` is in `containers.conf` (the `--hooks-dir` flag is not passed on to the restart process in Podman 4.9.3/5.0/5.4). Hook refuses when `/run/secrets` is not a tmpfs, and the container then fails to start.
- Verified only on Pop!_OS 24.04, Podman 4.9.3, runc, podman-compose 1.0.6, as Bart's user. crun, Ubuntu 26.04/Podman 5.x, and the dedicated agent user still to verify.
- Bart's decisions (all recorded in the design doc): passphrase-unlocked master store; push-and-keep delivery (agents keep secrets across relay disconnects); standard compose `secrets:` rewritten by the agent; no swap requirement, but a UI warning for hosts with swap; per-stack secrets; keep `env.json` for non-secret settings, labelled; automatic restart on rotation.
- Next: implementation phases listed under "Outstanding" in `docs/design/stack-secrets.md`.

Wheat change: "Stack-secret experiment and design".

## Relay hardening (2026-09-24)

Wheat change: "Harden agent relay: multiplexed sessions, heartbeat, cancellation, reconnect".

- Protocol v3 (`internal/protocol`): `cancel` message type, relative `timeout_ms`, error codes (`operation_failed`, `unknown_operation`, `busy`, `response_too_large`), `MaxInFlight = 32`, `ErrMessageTooLarge`.
- `internal/relay` split into `errors.go` (sentinels + `RemoteError`), `session.go` (server side: one reader per session, pending map by random ID, ping, cancel-on-give-up), `remote.go` (registry, `Serve(conn, certHostID)` blocks for the session, replacement), `agent.go` (concurrent dispatch, per-request contexts, cancel, pong/read deadline, waits for workers), `reconnect.go` (`RunAgent`, `DialAgent` with ctx and handshake timeout). Old `websocket.go`/`websocket_test.go` removed.
- Fixed along the way: agent with no stack manager used to report empty success for stack ops; `ControlHandler` could keep a dead host registered until the request context ended; `DialAgent` mutated the global `websocket.DefaultDialer`; `Hosts()` order was random; `Memory.ListStacks` returned success for unknown hosts.
- `server.Relay` takes `context.Context`; handlers pass `r.Context()` and map relay errors to 503/504 vs the route's own failure status.
- Agent main runs `RunAgent` under `signal.NotifyContext`.
- Tests: `internal/relay/relay_test.go` (19 tests + 2 benchmarks), `internal/server/stacks_test.go` status mapping table. `go test -race ./...` and `go vet ./...` pass; relay tests pass 5× under `-race`; `node --test tests/*.mjs` passes.
- Docs: `docs/design/architecture.md` has a new "Relay" section, updated checklist and measured budget.

Not done / next:
- Certificate allowlisting (new outstanding checklist item).
- The dev services must be restarted to pick up protocol v3 (server and all three agents together; v2 agents are refused).
- Bart has not yet chosen the stack-secret approach (A: Podman native secrets, B: server-stored encrypted secrets pushed as podman secrets at deploy, C: SOPS/age files decrypted on the agent).

## Earlier: Podman-only agent migration

### Status

- The Podman-only agent runtime, setup planner, bounded command adapter, and Compose executor are implemented.
- Target agents run native packages on Ubuntu 24.04 and 26.04 as a dedicated rootless user, require Podman 4.7 or later, and use a pinned `podman-compose`.
- The stack-secret workflow remains unimplemented pending its approved experiment.

## What was done

- Added Go module and server/agent command entrypoints.
- Added environment configuration validation, including admin password and mTLS file configuration.
- Added versioned bounded protocol envelope and tests.
- Replaced the original container-runtime adapter with a bounded Podman-only CLI adapter for host, container, image, volume, start, and stop operations.
- Added in-memory relay registry abstraction and authenticated HTTP route shape.
- Added expiring HttpOnly admin session cookies.
- Added `docs/design/architecture.md` with phase checklist and security/performance decisions.

## Verification

- `go test ./...` passes.
- `go vet ./...` passes.
- Linux amd64 and arm64 cross-builds pass with `GOFLAGS=-buildvcs=false` because this is a Wheat workspace and Go VCS stamping expects Git metadata.
- Wheat logical change: `Build Go service foundation`; latest captured revision `0757aedb86f64346189190c7876e52e14dddb6236f3c9e892982960549911bff`.

## Remaining work

- Harden the outbound mTLS WebSocket relay with reconnect, heartbeat, and pending-request cleanup.
- Run the setup command with interactive sudo and verify the dedicated rootless user and native Podman packages on Ubuntu 24.04 and 26.04; Podman >=4.7 is enforced at startup.
- Validate the configured distro `podman-compose` provider with live stack operations on supported releases.
- Run the approved stack-secret workflow experiment; secret support remains unimplemented until its result is adopted.
- Connect HTTP handlers to live agent sessions with request correlation, timeouts, cancellation, and disconnect cleanup.
- Add live Podman operations, then SolidJS frontend.
- Migrated `internal/integration/cluster_test.go` to three logical hosts sharing local Podman, authenticated through the real HTTP API, with inventory equality checks and automatic skip when Podman is unavailable.
- Added deterministic host prefixes and fake-agent start routing tests: `local-a-`, `local-b-`, and `local-c-`; a start request is dispatched only to the selected host and cross-host prefixes are rejected.
- Added a WebSocket relay prototype with agent hello, host registration, request IDs, deadline-bounded inventory/start calls, and in-process round-trip tests.
- Added a dedicated mTLS control listener path (default `:9443`), certificate-derived host identity (`sorry-host://HOST_ID` URI or `host-HOST_ID` DNS SAN), server/agent TLS configuration, and production command wiring. The web API remains plain HTTP for a reverse proxy.
- Added permission-checked JSON configuration files for server and agents, accepted via `--config`; environment loading remains a compatibility fallback. Config files reference key paths rather than embedding key material.
- Added `schemas/server-config.schema.json` and `schemas/agent-config.schema.json`; Go config decoding now rejects unknown fields and enforces semantic checks such as `wss://` agent URLs and restricted host IDs.
- Added local certificate provisioning: `sorry-portainer-server init` creates the private CA/server certificate/config; `sorry-portainer-server agent --create HOST_ID` creates a URI-SAN client certificate, key, CA copy, and agent config with restricted permissions.
- Added the first SolidJS/Vite UI shell: login view, fleet overview, host selection, container inventory, host-prefixed start action, responsive styling, and demo fallback when the API is offline.
- Added a `justfile` and user-systemd development setup. `just start` controls the server, three agents, and UI; ports are web 6200, UI 6201, and mTLS control 6243. Generated state lives in ignored `.dev/`.
- Agents invoke the local rootless Podman CLI without a shell and enforce their configured prefix: inventory filters by name, while start/stop resolve hash IDs and authorize the canonical container name before acting.
- Added read-only volume and image operations through the Podman adapter, relay, and HTTP routes. Volumes are prefix-filtered; images are returned for the full local installation.
- Added real start/stop lifecycle operations through the Podman adapter, relay, HTTP API, and host page UI. Both operations enforce the agent prefix and update the visible container state.
- Dev agents now use explicit isolated prefixes: `sorry-local-a-`, `sorry-local-b-`, and `sorry-local-c-`. Existing `.dev` configs were updated and all three agents restarted; host info reports the new prefixes.
- Migrated the Compose stack backend to bounded `podman compose` execution with an explicit distro `podman-compose` provider; stack UI, status refresh, Git sources, templates, live acceptance, and the secret workflow remain outstanding.
- mTLS reconnect/heartbeat, certificate allowlisting, and full disconnect cleanup remain outstanding.
- `README.md` and `NOTES.md` are existing scratch files and remain outside the captured change.
