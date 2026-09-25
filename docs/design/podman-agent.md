# Podman Agent — Design

Status: partial — rootless development Compose smoke verified on Ubuntu-compatible 24.04; secret experiment done; dedicated-user deployment and secret implementation outstanding
Owner: Bart
Last updated: 2026-09-24

## Implementation status

Tracking the gap between this design and the materialized implementation. Tick
items as they land and move them to Done.

### Done

- [x] **Platform decision — Podman-only agents.** The approved agent runtime is rootless Podman; Docker is not a supported agent runtime.
- [x] **Platform decision — supported host releases.** Agents target Ubuntu 24.04 and Ubuntu 26.04 using native distribution packages.
- [x] **Phase 1 — rootless setup command.** An explicit, idempotent plan/apply command installs native packages and configures a dedicated unprivileged service user, lingering, and its Podman socket.
- [x] **Phase 1 — Podman runtime boundary.** The agent uses bounded direct Podman CLI calls, enforces Podman 4.7 or later, and validates lifecycle ownership by resolved container name.
- [x] **Phase 1 — Compose companion.** Stack execution invokes `podman compose` with the configured distro `podman-compose` provider pinned explicitly for each operation.
- [x] **Phase 1 — development Compose smoke.** On Pop!_OS 24.04 under Bart's rootless user, Podman 4.9.3 and podman-compose 1.0.6 ran a disposable OCI sleeper service and `down` removed it. The automated rootless integration test exercises the same stack manager path.
- [x] **Open question — secret workflow experiment.** Run 2026-09-24. Podman native secrets always put values on disk. An OCI `createRuntime` hook that writes into a per-container tmpfs does not, and survives restarts when `hooks_dir` is in `containers.conf`. The design and remaining decisions are in `docs/design/stack-secrets.md`.

### Outstanding

- [ ] **Phase 1 — deployment verification.** Test lifecycle under the dedicated service user and on Ubuntu 26.04; the development services currently run as Bart, not the newly provisioned service user.
- [ ] **Phase 1 — authenticated development API stack smoke.** Exercise save/up/down through the running development agents and master; the local test account/password is not available to the agent in this session.
- [ ] **Stack secrets — setup integration.** The setup planner must write the agent user's `containers.conf` `hooks_dir` and the secret hook JSON (see `docs/design/stack-secrets.md`). Secret handling itself is still unimplemented.

## Why this exists

The agent is the process that reaches the host container runtime, so its runtime
and privilege model set the operational and security boundary for every managed
host. The project supports Podman agents only. This removes a dual-runtime
support matrix and makes the rootless, per-user runtime the standard deployment
model rather than an optional configuration.

This document is the deployment contract for the migration. It does not make the
central server a Podman client: the server continues to communicate with agents
through the existing bounded, mutually authenticated relay protocol.

## Goals

- Run every supported agent against rootless Podman 4.7 or newer.
- Support Ubuntu 24.04 and Ubuntu 26.04 through their native packages.
- Run each agent as a dedicated unprivileged user, isolated from administrator
  login accounts and from a rootful container socket.
- Use a version-pinned `podman-compose` for stack lifecycle operations.
- Keep container, image, volume, network, and stack operations bounded and
  cancellable across the relay.

## Non-goals (v1)

- Supporting Docker Engine, Docker Compose, or a Docker compatibility socket as
  an agent runtime.
- Supporting non-Ubuntu agent hosts.
- Rootful Podman as a fallback deployment mode.
- Defining a stack-secret workflow before the pending experiment establishes a
  safe operational model.

## Host and runtime model

Each host has a dedicated service account, referred to here as the agent user.
The agent service runs as that user and accesses only that user's rootless Podman
runtime. Installation must satisfy the operating system requirements for
rootless containers, including subordinate UID/GID mappings and a persistent
user runtime suitable for the service manager. The deployment procedure must
validate these prerequisites rather than silently falling back to rootful
execution.

The agent validates the installed Podman version before accepting work. Versions
older than 4.7 are unsupported. Ubuntu 24.04 and 26.04 use their native package
sources; the provisioning implementation records the exact packages and their
release-specific prerequisites.

## Compose stacks

Stack operations use `podman compose`, not Docker Compose. The provisioning
path installs Ubuntu's `podman-compose` package, and every operation sets
`PODMAN_COMPOSE_PROVIDER` to the configured absolute provider path rather than
letting Podman select whichever provider appears first. The provider tested on
Ubuntu-compatible 24.04 accepts `-p` and `-f` but not Docker Compose's
`--project-directory`; the executor sets its working directory instead.
Package upgrades remain under the supported Ubuntu repository's lifecycle;
capability tests guard the runtime boundary.

Stack definitions and runtime state remain scoped to the agent user. The service
must execute `podman-compose` with the same account and rootless runtime as the
agent, so lifecycle requests cannot accidentally target a rootful or another
user's containers.

## Secrets

No secret workflow is implemented by this migration. In particular, stack
configuration must not treat plaintext files, environment values, or a
rootful-runtime workaround as an approved secret mechanism. The pending
experiment must establish the storage, injection, rotation, access-control, and
backup model before implementation is planned.

Follow-up (2026-09-24): the experiment ran. Podman's own secrets are ruled out
because they always write the value into the container's `userdata` on disk.
The chosen mechanism is an OCI hook, run by the agent binary, that writes
secrets into a per-container tmpfs before the app starts. The master stores the
values and the agent keeps them only in memory. See
`docs/design/stack-secrets.md`. For this document it means the agent user's
`containers.conf` must set `hooks_dir`, because a `--hooks-dir` flag is not
passed on to Podman's restart-policy process.

## Failure and performance behavior

Runtime detection and version validation occur once at agent startup and are
reported as a clear unavailable-host condition. They must not be re-run per
container or per stack operation. Inventory and lifecycle requests retain the
relay's deadlines, payload bounds, cancellation, and disconnect cleanup
requirements. Commands that produce logs or Compose output must stream with
backpressure rather than accumulate unbounded output in the agent.

## Phasing

Phase 1 provisions the rootless agent account and native packages, adds Podman
runtime/version validation, and proves core lifecycle operations on both
supported Ubuntu releases. Phase 2 pins and validates `podman-compose` for
stack operations. Secret support is intentionally outside both phases until the
experiment resolves the open question.

## Open questions for review

- Which `podman-compose` version and compatibility test matrix should be pinned
  for Ubuntu 24.04 and 26.04?
- What secret workflow does the pending experiment validate, including rotation
  and backup behavior?
- Which agent-health fields expose Podman and `podman-compose` versions without
  disclosing sensitive host configuration?

## References

- `docs/design/architecture.md` — central service, relay, and UI boundaries.
- Ubuntu 24.04 and Ubuntu 26.04 native package documentation — deployment
  package selection and rootless prerequisites.
- Podman rootless and version documentation — runtime prerequisites and the
  4.7 minimum.
