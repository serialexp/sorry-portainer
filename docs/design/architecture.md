# sorry-portainer Architecture — Design

Status: partial — Podman agent/runtime migration landed; relay hardening and secrets outstanding
Owner: Bart
Last updated: 2026-09-22

## Implementation status

### Done

- [x] **Phase 0 — Go foundation.** Module, server and agent entrypoints, environment configuration, versioned protocol envelope, and package boundaries exist.
- [x] **Phase 0 — agent runtime boundary.** The agent-side Podman client is isolated behind a small interface, uses bounded direct argv execution, and maps host/container inventory into protocol types.
- [x] **Phase 0 — HTTP session boundary.** Health, password login, expiring HttpOnly session cookies, authenticated host/container route shape, and bounded request decoding exist.
- [x] **Phase 0 — local verification.** Protocol tests and `go test ./...` pass.
- [x] **Phase 0 — fake local cluster.** Integration tests model three logical hosts sharing one daemon and verify host-prefix start routing with fakes.

### Outstanding

- [ ] **Phase 1 — mTLS agent transport.** Implement outbound WebSocket sessions, certificate identity mapping, hello/heartbeat, reconnect, and bounded message handling.
- [x] **Phase 1 — WebSocket relay prototype.** Add versioned agent hello, host registration, request IDs, deadline-bounded inventory/start calls, and in-process round-trip coverage.
- [x] **Phase 1 — mTLS control listener.** Dedicated control listener defaults to `:9443`, requires client certificates, and derives host identity from a `sorry-host://HOST_ID` URI or `host-HOST_ID` DNS SAN. The web API remains plain HTTP for reverse-proxy TLS termination.
- [x] **Phase 1 — file configuration.** Server and agent commands accept permission-checked JSON config files via `--config`; environment loading remains a compatibility fallback, and certificate/private-key values are referenced by path.
- [x] **Phase 1 — config schemas.** JSON Schema documents under `schemas/` describe the server and agent files; Go uses strict decoding and repeats semantic checks at startup.
- [x] **Phase 1 — local certificate provisioning.** `server init` creates a private control CA, server certificate, server config, and restricted state directory; `server agent --create HOST_ID` creates a host URI-SAN certificate, client key, CA copy, and agent config.
- [ ] **Phase 1 — live relay hardening.** Add reconnect/heartbeat, configured certificate allowlisting, and complete pending-request cleanup on disconnect.
- [x] **Phase 1 — Podman agent runtime.** Run bounded Podman operations over the live relay, enforce Podman 4.7 or later, and authorize lifecycle actions by resolved container ownership.
- [x] **Phase 1 — rootless Ubuntu setup.** Add an explicit plan/apply path for Ubuntu-compatible 24.04 and 26.04 native packages under a dedicated rootless user; live cross-release acceptance remains outstanding.
- [x] **Phase 2 — Podman Compose stacks.** Invoke `podman compose` with an explicitly configured distro `podman-compose` provider while preserving bounded host-scoped stack lifecycle operations. Git sources, templates, UI stack editor, and live provider acceptance remain outstanding.
- [ ] **Phase 2 — Podman operations.** Add logs, exec, images, volumes, networks, and harden `podman-compose` stack status/output with streaming and bounded resource use.
- [ ] **Phase 2 — stack-secret workflow.** Leave secret support unimplemented until the approved experiment selects a storage, injection, rotation, access-control, and backup model.
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

The relay envelope includes a protocol version, random request ID, message type,
operation, host identity where relevant, bounded JSON payload, and structured
error. The server selects the host from authenticated configuration/connection
identity; it must not trust an arbitrary host ID supplied by an agent as
authority.

## Security

The first admin password is supplied by `SORRY_PORTAINER_ADMIN_PASSWORD` and is
never written to disk or logs. Login uses constant-time comparison and creates a
random opaque in-memory session cookie. Sessions expire and are invalidated on
server restart until a durable session store is designed.

Agent mTLS uses a configured CA and server/client certificates. The server maps
the verified client certificate identity to a configured host ID. Certificate
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
whole-state serialization. Add a benchmark when the live relay lands.

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
  rotation, access control, and backups?
- What durable store should hold host records, certificate fingerprints, and
  session state?
