# Podman Agent — Design

Status: partial — rootless development Compose smoke verified on Ubuntu-compatible 24.04; stack secrets, agent-restart container survival and reboot bring-up implemented; dedicated-user deployment outstanding
Owner: Bart
Last updated: 2026-09-25

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

- [x] **Stack secrets — setup integration.** `setup` runs `setup-hooks-conf` as the agent user, writing `~/.config/containers/containers.conf.d/50-sorry-portainer.conf` with `hooks_dir`; the agent writes its hook JSON (default `~/.local/share/sorry-portainer/oci-hooks`, config `oci_hooks_dir`) and opens its secret socket at startup, and exits if either fails. Development agents get the drop-in through `CONTAINERS_CONF_OVERRIDE`. Details in `docs/design/stack-secrets.md`.
- [x] **Agent stop kills stack containers.** Podman keeps `conmon` in the calling systemd unit's cgroup when `INVOCATION_ID` is set, so stopping or restarting the agent service killed every container the agent started (seen on the development agents 2026-09-24). Bart (2026-09-25): the agent unsets `INVOCATION_ID` at startup (`cmd/sorry-portainer-agent/main.go`), as Podman's API service does. Verified with a throwaway systemd unit and by restarting the development agent with a stack running.
- [x] **Stacks come back after a host reboot (extended beyond the original plan).** Bart (2026-09-25): the agent records each stack's desired state and the boot it last came up in, and brings `up` stacks back after a reboot (secret stacks once the master pushes their secrets). Details in `docs/design/stack-secrets.md`, "Follow-up (2026-09-25)". This replaces enabling Podman's `podman-restart` service.

### Outstanding

- [ ] **Phase 1 — deployment verification.** Test lifecycle under the dedicated service user and on Ubuntu 26.04; the development services currently run as Bart, not the newly provisioned service user.
- [ ] **Phase 1 — authenticated development API stack smoke.** Save and up (with stack secrets) were exercised through the running development agents, master and UI on 2026-09-24; `down` through the API is still untested live (the UI now has a Stop button; its API call is covered by `tests/stack-api.test.mjs`).
- [ ] **Decision needed — one-off services after a reboot.** Bringing a stack back also starts containers that had exited on purpose; Docker only restarts containers with a restart policy. Options in `docs/design/stack-secrets.md`.
- [ ] **`down` leaves the project network.** podman-compose 1.0.6 does not remove `<project>_default`; stacks leak one network per `down`.

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

Follow-up (2026-09-24, later): implemented. Setup writes the `hooks_dir`
drop-in as the agent user (never as root), and the agent writes its hook JSON
and opens its secret socket in `$XDG_RUNTIME_DIR` at every start. The live
rootless test confirmed the hook also runs on restarts by Podman's restart
policy. `env.json` stays for non-secret settings only.

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
  and backup behavior? (Answered 2026-09-24 in `docs/design/stack-secrets.md`;
  backups of the master's sealed store are still undecided.)
- Which agent-health fields expose Podman and `podman-compose` versions without
  disclosing sensitive host configuration?

## References

- `docs/design/architecture.md` — central service, relay, and UI boundaries.
- Ubuntu 24.04 and Ubuntu 26.04 native package documentation — deployment
  package selection and rootless prerequisites.
- Podman rootless and version documentation — runtime prerequisites and the
  4.7 minimum.
