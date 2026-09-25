# sorry-portainer Architecture — Design

Status: partial — Podman agent/runtime, relay hardening and stack secrets landed; certificate allowlisting outstanding
Owner: Bart
Last updated: 2026-09-24

## Implementation status

### Done

- [x] **Phase 0 — Go foundation.** Module, server and agent entrypoints, environment configuration, versioned protocol envelope, and package boundaries exist.
- [x] **Phase 0 — agent runtime boundary.** The agent-side Podman client is isolated behind a small interface, uses bounded direct argv execution, and maps host/container inventory into protocol types.
- [x] **Phase 0 — HTTP session boundary.** Health, password login, expiring HttpOnly session cookies, authenticated host/container route shape, and bounded request decoding exist.
- [x] **Phase 0 — local verification.** Protocol tests and `go test ./...` pass.
- [x] **Phase 0 — fake local cluster.** Integration tests model three logical hosts sharing one daemon and verify host-prefix start routing with fakes.
- [x] **Phase 2 — stack-secret workflow.** Implemented 2026-09-24: an encrypted, passphrase-unlocked store on the master; protocol v4 `secrets.sync`/`secrets.retain` pushes into agent memory; an OCI `createRuntime` hook writes values into each container's `/run/secrets` tmpfs. Remaining work and two open decisions are in `docs/design/stack-secrets.md`.

### Outstanding

- [x] **Phase 1 — mTLS agent transport.** Outbound WebSocket sessions, certificate identity mapping, hello/heartbeat, reconnect, and bounded message handling. See [Relay](#relay).
- [x] **Phase 1 — WebSocket relay prototype.** Add versioned agent hello, host registration, request IDs, deadline-bounded inventory/start calls, and in-process round-trip coverage.
- [x] **Phase 1 — mTLS control listener.** Dedicated control listener defaults to `:9443`, requires client certificates, and derives host identity from a `sorry-host://HOST_ID` URI or `host-HOST_ID` DNS SAN. The web API remains plain HTTP for reverse-proxy TLS termination.
- [x] **Phase 1 — file configuration.** Server and agent commands accept permission-checked JSON config files via `--config`; environment loading remains a compatibility fallback, and certificate/private-key values are referenced by path.
- [x] **Phase 1 — config schemas.** JSON Schema documents under `schemas/` describe the server and agent files; Go uses strict decoding and repeats semantic checks at startup.
- [x] **Phase 1 — local certificate provisioning.** `server init` creates a private control CA, server certificate, server config, and restricted state directory; `server agent --create HOST_ID` creates a host URI-SAN certificate, client key, CA copy, and agent config.
- [x] **Phase 1 — live relay hardening.** Protocol v3: multiplexed concurrent requests over one reader per session, server heartbeat on both ends, request cancellation reaching the agent, a 32-request in-flight window, pending-request cleanup and prompt host removal on disconnect, session replacement, and agent reconnect with jittered backoff. Relay failures map to 503/504 separately from operation failures.
- [ ] **Phase 1 — certificate allowlisting.** Any certificate the control CA signed is accepted today; the server should only accept configured (or enrolled) host IDs, so a leaked CA-signed certificate for an unknown host is refused.
- [x] **Phase 1 — Podman agent runtime.** Run bounded Podman operations over the live relay, enforce Podman 4.7 or later, and authorize lifecycle actions by resolved container ownership.
- [x] **Phase 1 — rootless Ubuntu setup.** Add an explicit plan/apply path for Ubuntu-compatible 24.04 and 26.04 native packages under a dedicated rootless user; live cross-release acceptance remains outstanding.
- [x] **Phase 2 — Podman Compose stacks.** Invoke `podman compose` with an explicitly configured distro `podman-compose` provider while preserving bounded host-scoped stack lifecycle operations. Git sources, templates, UI stack editor, and live provider acceptance remain outstanding.
- [ ] **Phase 2 — Podman operations.** Add logs, exec, images, volumes, networks, and harden `podman-compose` stack status/output with streaming and bounded resource use.
- [x] **Phase 2 — frontend shell.** Add a SolidJS/Vite dashboard with centralized local state, login shell, host selection, container inventory, and host-routed start controls; it falls back to demo data while the API is offline.
- [x] **Phase 2 — local development services.** Add a Wheat-ignored `.dev` state area, user-level systemd unit generation, and `just` recipes for server, three local agents, and UI on ports 6200/6201/6243.
- [x] **Legacy — Docker agent filtering.** The superseded Docker agents filter inventory/start operations to their configured host prefix; local-a/b/c integration tests and live dev-agent checks cover that historical boundary.
- [x] **Legacy — Docker volumes and images.** The superseded Docker implementation has read-only host volume inventory filtered by prefix and full image inventory without prefix filtering.
- [x] **Legacy — Docker container lifecycle.** The superseded Docker implementation has host-prefix-protected start and stop operations through the agent, HTTP API, and host detail UI.
- [x] **Legacy — isolated development namespaces.** Development agents use `sorry-local-a-`, `sorry-local-b-`, and `sorry-local-c-` prefixes so shared-daemon tests cannot be confused with unrelated legacy Docker resources.
- [ ] **Phase 2 — frontend integration.** Replace the demo fallback with live API loading, add stores/modules as the UI grows, and add browser/component tests.
- [ ] **Phase 3 — durable administration.** Persist host enrollment and metadata, add certificate rotation/revocation, durable sessions, and multi-user RBAC.

## Why this exists

Portainer is a useful reference product but its operational model is not the one
we want to preserve. This project manages multiple Podman hosts through small
outbound agents, so the central service never needs direct access to a host
container-runtime socket or inbound firewall exceptions on every host.

The system is deliberately split into a central HTTP service, a per-host agent,
and a narrow versioned relay protocol. Podman details stay on the host. The
central service handles browser authentication, host selection, authorization,
and presentation. This boundary also makes the agent and server independently
cross-compilable for common architectures.

Agents are Podman-only. They run as a dedicated rootless user on Ubuntu 24.04 or
26.04 using native packages, require Podman 4.7 or newer, and use a pinned
`podman-compose` for stacks. Stack-secret support remains unimplemented pending
the approved experiment; it is not a deployment fallback or an implied feature.

Follow-up (2026-09-24): stack secrets are implemented as designed in
`docs/design/stack-secrets.md`. Values live encrypted on the master and in
plain form only in agent memory and container tmpfs.

## Goals

- Manage multiple Podman hosts from one web service.
- Keep host connectivity outbound and authenticate it with mTLS.
- Keep Podman access and sockets with the dedicated rootless user running the
  agent.
- Make every network and Podman operation bounded, cancellable, and testable.
- Provide a small HTTP API that the SolidJS frontend can consume without
  knowing relay or Podman details.
- Prefer static Go binaries for Linux amd64 and arm64 deployments.

## Non-goals (initial slice)

- Kubernetes, Docker Engine, Docker Compose, Docker Swarm, registries, or cloud
  control planes.
- Rootful Podman or a Docker compatibility socket as an agent-runtime fallback.
- Agent access to central administrative credentials or lease authority.
- Multi-user RBAC before the single-admin path is exercised.
- A stack-secret workflow before the pending experiment establishes one.
- Unbounded log buffering or browser-side direct Podman connections.

## Components

```text
Browser / SolidJS
        |
   HTTPS + cookie session
        |
Central Go server ---- in-memory agent registry ---- outbound mTLS WebSocket
                                                       |
                                           Go agent + rootless Podman socket
```

The first route contract is:

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| GET | `/healthz` | none | process health |
| POST | `/api/session/login` | password | establish an opaque expiring cookie session |
| GET | `/api/hosts` | session | list known host status |
| GET | `/api/hosts/{hostID}/containers` | session | list all containers through that host's agent |

Stack secrets added (2026-09-24), all behind the session:
`GET /api/secrets/status`, `POST /api/secrets/{initialize,unlock,lock}`, and
`GET /api/hosts/{hostID}/stacks/{stack}/secrets` (names, sizes, delivery
state; works while locked) with `PUT`/`DELETE …/secrets/{name}` (write-only
values; need the store unlocked, else 423).

The relay envelope includes a protocol version, random request ID, message type,
operation, host identity where relevant, bounded JSON payload, and structured
error. The server selects the host from authenticated configuration/connection
identity; it must not trust an arbitrary host ID supplied by an agent as
authority.

## Relay

One WebSocket per agent carries protocol v4 messages (`hello`, `request`,
`response`, `cancel`), each at most 4 MiB encoded. (v4 added the stack-secret
operations `secrets.sync` and `secrets.retain`, and the `hello` fields
`swap_active`, `secrets_ready` and `secrets_problem`.)

- **Identity.** The control handler hands the hijacked connection and the
  certificate's host ID to `relay.Remote.Serve`, which blocks for the life of the
  session. The first frame must be a `hello` naming that same host ID. A new
  session for a host replaces the old one and fails the old one's pending
  requests; the old session's exit never unregisters its replacement.
- **Multiplexing.** Each session has one reader goroutine. Requests carry random
  128-bit IDs, and the reader hands each response to the caller waiting on that
  ID. A slow operation never blocks a fast one, and a response arriving after its
  caller gave up is dropped rather than delivered to the next request.
- **Deadlines and cancellation.** Every call runs under the HTTP request's
  context plus an operation budget (10 s, or 6 min for stack up/down/restart).
  The request carries the remaining budget as a relative `timeout_ms`, so host
  clock skew does not matter. When the caller's context ends first, the server
  sends `cancel` and the agent cancels that operation's context.
- **Back-pressure.** At most 32 requests are in flight per host. The server
  refuses a 33rd with `ErrHostBusy`; the agent refuses beyond its own 32
  (possible briefly while abandoned operations wind down) with a retryable
  `busy` error that maps to the same status.
- **Liveness.** The server pings every 15 s. Both ends treat three silent
  intervals as a dead connection. Writes have a 10 s deadline, so a stalled peer
  cannot hold the write lock.
- **Disconnect.** When a session ends, every pending request fails with
  `ErrDisconnected` and the host leaves the registry immediately. On the agent,
  `Serve` cancels in-flight operations and waits for them before returning, so no
  Podman child outlives its session.
- **Reconnect.** `relay.RunAgent` redials with backoff from 1 s doubling to 30 s,
  jittered to between half and all of the current step, and resets once a
  session has lasted a minute. SIGTERM/SIGINT stop it cleanly.
- **Oversize.** An oversized request fails on its own. An oversized response
  becomes a `response_too_large` error. Neither closes the session.

HTTP status mapping: host unavailable, busy, disconnected, or client gone →
503; relay timeout → 504; an operation the agent ran and reported failed → the
route's own status (409 for stack save, 404 for stack inspect/versions, 502
otherwise); anything else → 502.

## Security

The first admin password is supplied by `SORRY_PORTAINER_ADMIN_PASSWORD` and is
never written to disk or logs. Login uses constant-time comparison and creates a
random opaque in-memory session cookie. Sessions expire and are invalidated on
server restart until a durable session store is designed.

Agent mTLS uses a configured CA and server/client certificates. The server takes
the host ID from the verified client certificate and requires the agent's hello
to claim the same ID. Restricting that to configured host IDs is outstanding
(see the allowlisting item above). Certificate
enrollment, rotation, and revocation are later administrative features; no
agent protocol is allowed to grant an administrative lease.

## Performance and failure behavior

Container inventory is one Podman API call and one relay request, not one request
per container. Podman version and rootless-runtime validation happen once at
agent startup, not per operation. Relay requests have deadlines and bounded
payload sizes. A connection loss fails all pending requests for that agent and
marks it offline; it must not leave goroutines or promises waiting indefinitely.
Future logs, exec operations, and `podman-compose` output must stream with
backpressure instead of collecting output in memory.

Representative initial budget: a 1,000-container inventory should complete as a
single bounded request without per-container durable synchronization or repeated
whole-state serialization, in under 50 ms of relay overhead.
`BenchmarkContainersInventory1000` in `internal/relay` measures the full
agent-encode → WebSocket → server-decode round trip over loopback at about
3 ms and 1.9 MB allocated per call (Ryzen 9 9900X, 2026-09-24). This is
dominated by JSON encoding of the roughly 250 KB payload.
`BenchmarkConcurrentSmallRequests` sustains about 8 µs per request (roughly
120,000 requests/s) with 16 concurrent callers on one session.

## Phasing

Phase 1 completes the relay and makes inventory routes usable against a real
rootless Podman agent on the supported Ubuntu releases. Phase 2 adds day-to-day
Podman operations, a pinned `podman-compose` stack path, and the SolidJS
frontend. The stack-secret workflow remains unimplemented until its experiment
is complete. Phase 3 adds durable administration and access control only after
the single-admin, multi-host flow is stable.

## Open questions

- Should host enrollment be declarative configuration first or a browser-driven
  certificate bootstrap flow?
- Should browser auth remain cookie-based when multi-user RBAC is added?
- Is WebSocket the long-term relay transport, or should measured streaming needs
  move it to HTTP/2 or QUIC?
- Which `podman-compose` version should be pinned after compatibility testing on
  Ubuntu 24.04 and 26.04?
- What does the pending stack-secret experiment establish for storage, injection,
  rotation, access control, and backups? (Answered 2026-09-24 in
  `docs/design/stack-secrets.md`, except backups of the master's sealed store.)
- What durable store should hold host records, certificate fingerprints, and
  session state?
