# Stack Secrets — Design

Status: draft, not yet implemented — experiment done; all design decisions made; implementation not started
Owner: Bart
Last updated: 2026-09-24

## Implementation status

Tracking the gap between this design and the materialized implementation. Tick
items as they land and move them to Done.

### Done

- [x] **Experiment — Podman native secrets.** Showed that rootless Podman writes every secret value to disk under the container's `userdata`, whatever the secret driver. Rejected. See [Experiment results](#experiment-results).
- [x] **Experiment — tmpfs injection through an OCI hook.** A `createRuntime` hook writes secrets into the container's own tmpfs before the app starts, on every start path, and nothing reaches disk. Chosen mechanism.
- [x] **Requirement — where secrets may live.** Bart (2026-09-24): secrets must not be kept on agent disks; they may live only in container memory or on the master.
- [x] **Decision — master at-rest protection.** Bart (2026-09-24): the key comes from a passphrase typed at server start (open question 1b). Until someone unlocks it, the master cannot deliver secrets.
- [x] **Decision — delivery to agents.** Bart (2026-09-24): push and keep (open question 2a). The master sends a stack's secrets at deploy and on every agent (re)connect; the agent keeps them in memory.
- [x] **Decision — compose syntax.** Bart (2026-09-24): standard compose `secrets:`, rewritten by the agent (open question 3a).
- [x] **Decision — swap.** Bart (2026-09-24): don't require anything, but the UI warns about agents whose host has swap enabled (a variant not in the listed options).

- [x] **Decision — scope.** Bart (2026-09-24): secrets belong to one stack (open question 5, per stack).
- [x] **Decision — existing `env.json`.** Bart (2026-09-24): keep the plaintext stack environment for ordinary settings, clearly labelled in the UI and API as not for secrets (open question 6).
- [x] **Decision — rotation.** Bart (2026-09-24): the agent restarts affected containers automatically when a new value arrives (open question 7).

### Outstanding

- [ ] **Phase 1 — master secret store.** Encrypted storage keyed from a start-time passphrase, an unlock step (API + UI) and a clear "locked" state, admin API to create, replace and delete (values write-only, never returned), and a record of which stacks use which secret.
- [ ] **Phase 1 — swap warning.** The agent reports whether its host has active swap (and whether it is encrypted, if detectable); the UI shows a warning on those hosts.
- [ ] **Phase 1 — relay delivery.** Protocol messages that carry secret values to the agent; values never logged; the agent holds them only in memory and wipes them on forget.
- [ ] **Phase 1 — agent hook.** An `oci-hook` subcommand of the agent binary plus an agent-user Unix socket; fail closed; refuses to write unless `/run/secrets` is a tmpfs in the container's mount namespace; no symlink following.
- [ ] **Phase 1 — setup integration.** The setup planner writes the agent user's `containers.conf` `hooks_dir` and the hook JSON; the agent checks both at startup.
- [ ] **Phase 1 — compose rewrite.** Services that use secrets get the hook annotation and a `/run/secrets` tmpfs; Podman-native `secrets:` never reaches `podman-compose`.
- [ ] **Phase 1 — label `env.json` as non-secret.** The UI and API say plainly that stack environment values are stored in plaintext on the host.
- [ ] **Phase 2 — rotation.** Replacing a value pushes it to the agent, which then restarts the affected containers so the hook runs again.
- [ ] **Phase 2 — host reboot and agent restart.** Containers that start before the agent has its secrets fail loudly; the agent brings them up once the secrets arrive.
- [ ] **Verification — crun.** The experiment ran on runc only; Ubuntu hosts may use crun, whose hook-time paths could differ.
- [ ] **Verification — Ubuntu 26.04 / Podman 5.x and the dedicated agent user.** Re-run `experiments/stack-secrets/` there.
- [ ] **Defer — environment-variable secrets.** Not possible without disk (see below). Apps read files, for example through `*_FILE` variables.

## Why this exists

The project started because we want to handle secrets more safely than
Portainer does. Today the stack manager stores a stack's environment as a
plaintext `env.json` on the agent and passes it to `podman-compose`, which is
not acceptable for secrets. The requirement is that a secret value never sits on
an agent host's disk: it may exist on the master and in the memory of the
container that uses it. This doc records what the experiment showed Podman can
and cannot do, and the mechanism we will build.

## Goals

- Secret values never written to an agent host's disk: not in Podman storage,
  OCI config, logs, the agent state directory, or `podman inspect` output.
- Works for every way a container starts: `podman-compose up`, a start from the
  UI, and Podman's own restart policies.
- Fail loudly: a container that needs a secret the agent doesn't have must not
  start without it.
- Secrets delivered only over the existing mTLS relay.

## Non-goals (v1)

- Secrets as environment variables (impossible without disk; see results).
- Protecting a secret from the running container's own processes or from
  someone with the agent user's shell (they can already exec into the
  container).
- External secret managers (Vault and similar) as the master's store, unless
  chosen in open question 1.

## Experiment results

Run on 2026-09-24: Pop!_OS 24.04 (Ubuntu-compatible), rootless Podman 4.9.3,
runc, podman-compose 1.0.6, kernel 7.0. A random throwaway canary stood in
for the secret, and after each step every file under Podman's graphroot (ext4)
and runroot (tmpfs) was searched for it. The scripts are in
`experiments/stack-secrets/`.

| Approach | Where the value ended up | Survives container restart |
| --- | --- | --- |
| Podman secret, `type=mount`, driver storing only in tmpfs | Copied at `podman create` to `graphroot/overlay-containers/<id>/userdata/secrets/<name>` (disk) and bind-mounted into `/run/secrets` | Yes (the disk copy) |
| Podman secret, `type=env` | Written to `userdata/config.json` (disk) at start; visible in `podman inspect` | No: fails to start when the driver no longer has the value |
| Either of the above with `--transient-store` | Same disk locations; transient store does not move `userdata` | — |
| `podman init` → write into the container's tmpfs via `/proc/<pid>/root` → `podman start` | Only the container's own tmpfs | No: the tmpfs is new on every start |
| **OCI `createRuntime` (or `prestart`) hook writing into the container's tmpfs** | **Only the container's own tmpfs** | **Yes: the hook runs on every start** |

Details that shape the design:

- Current Podman `main` does the same as 4.9.3 (`SecretsPath` is
  `StaticDir/secrets`), so this is not a version bug we can wait out.
- At `createRuntime`/`prestart` the container is not yet pivoted.
  `/proc/<pid>/root` is the host root inside the container's mount namespace,
  so the tmpfs is at `/proc/<pid>/root/<rootfs>/run/secrets`, where `<rootfs>` is
  `root.path` from the bundle's `config.json`. `createContainer` could not reach
  it (permission denied).
- Hooks given with `--hooks-dir` on the command line are **not** used by
  Podman's restart-policy process in Podman 4.9.3, 5.0 or 5.4
  (`CreateExitCommandArgs` does not pass it on). When `hooks_dir` is set in
  `containers.conf`, all four starts of an `on-failure` container ran the hook.
- `podman-compose` 1.0.6 passes service `annotations:` and `tmpfs:` through.
  The end-to-end compose test (create, then a manual stop/start) injected on
  every start, with nothing on disk and nothing in `inspect`.
- Podman refuses `noswap` on rootless tmpfs mounts.
- A Podman secret whose driver store has vanished (say, after a reboot) cannot be
  removed with `podman secret rm` or replaced with `--replace` until the store is
  recreated. This is one more reason not to depend on Podman secrets.
- A shell-driver or file-driver secret store in tmpfs keeps the value off disk
  only until `podman create`, which copies it to disk.

## Mechanism

```text
master (encrypted store) --mTLS relay--> agent memory
                                             |  Unix socket (agent user only)
podman start / compose up / restart policy --> runc --> OCI hook (agent binary)
                                                           |
                                    /proc/<pid>/root/<rootfs>/run/secrets/<name>  (container tmpfs)
```

1. The setup planner writes, for the agent user:
   - `~/.config/containers/containers.conf` with `[engine] hooks_dir` pointing
     at a directory the agent owns.
   - A hook JSON there: path = the agent binary, args `oci-hook`, stage
     `createRuntime`, only when the annotation `io.sorry-portainer.secrets` is
     set.

   The agent checks at startup that both are in place and refuses stacks with
   secrets otherwise.
2. When a stack is saved or brought up, every service that uses secrets gets:
   - the annotation listing its secret names;
   - `tmpfs: /run/secrets:rw,size=…,mode=0700`.

   Podman-native `secrets:` never reaches `podman-compose`, which would
   otherwise create disk-backed Podman secrets.
3. `oci-hook` reads the OCI state from stdin (bounded) and the bundle's
   `config.json`. It asks the agent over `$XDG_RUNTIME_DIR/sorry-portainer/agent.sock`
   for the secrets of that container ID. The agent answers only for names the
   container's stack is allowed to use, looked up by the compose project and
   service labels, not by trusting the annotation alone.
4. Before writing anything, the hook checks that `<rootfs>/run/secrets` is a
   tmpfs mount in the container's mount namespace (`/proc/<pid>/mountinfo`).
   Without this, a missing tmpfs would put the secret in the container's
   writable layer on disk. It then creates each file with mode 0400 using
   no-symlink resolution (`openat2` with `RESOLVE_NO_SYMLINKS`), because the
   image controls the paths under the rootfs.
5. Any failure exits non-zero, and runc then refuses to start the container.

With a passphrase-locked master and push-and-keep delivery, an agent must
**not** drop its in-memory secrets when the relay disconnects. After a master
restart the master stays locked until someone unlocks it, and containers on the
hosts must keep restarting in the meantime. An agent's memory is only filled
again, after an agent restart or host reboot, once the master is unlocked and
the agent reconnects. Until then, containers that need secrets fail to start and
the UI must show why.

## Open questions for review

1. **Master at-rest protection.**
   - (a) Encrypt with a key file on the master: starts unattended, but anyone
     who has both the master's disk and the key file has the secrets.
   - (b) Key derived from a passphrase typed when the server starts: nothing
     usable on disk, but every master restart needs a person before
     containers with secrets can (re)start.
   - (c) External KMS or secret manager holding the key or the values.
2. **Delivery.**
   - (a) The master pushes a stack's secrets to the agent at deploy time and on
     every agent (re)connect. The agent keeps them in memory, so restarts work
     while the master is down.
   - (b) The hook asks the agent, which asks the master at that moment. Fewer
     copies in agent memory, but no container with secrets can start while the
     master is unreachable.
   - (c) (b) plus an in-memory cache.
3. **Compose syntax.**
   - (a) Standard compose `secrets:` (top-level plus per-service), rewritten by
     the agent; `file:` and `environment:` sources are rejected.
   - (b) A project extension such as `x-sorry-secrets: [name, …]` per service,
     leaving standard `secrets:` unsupported.
4. **Swap.** tmpfs pages and agent memory can be swapped out, and rootless
   Podman refuses `noswap`.
   - (a) Setup requires no swap or encrypted swap, and the agent checks it at
     startup.
   - (b) Document the risk only.
   - (c) (a) for swap, plus `mlock` for the agent's own copy.
5. **Scope.** Secrets per stack, or a per-host/global library that stacks
   reference by name.
6. **Existing plaintext stack environment (`env.json`).** Keep it for
   non-secret configuration, clearly labelled, or remove it.
7. **Rotation.** After a value changes, restart affected containers
   automatically or show "restart needed" in the UI.

Decisions (Bart, 2026-09-24): 1 → (b) passphrase at start; 2 → (a) push and
keep; 3 → (a) standard `secrets:`; 4 → no requirement, but the UI warns about
agents with swap; 5 → per stack; 6 → keep `env.json` for non-secret settings,
clearly labelled; 7 → automatic restart.

## References

- `docs/design/architecture.md` — relay protocol and security boundaries.
- `docs/design/podman-agent.md` — rootless agent user and setup planner.
- `experiments/stack-secrets/` — the reproducible experiment.
- Podman `libpod/container_internal_common.go` (`createSecretMountDir`) and
  `pkg/specgenutil/util.go` (`CreateExitCommandArgs`), checked on `main`,
  v4.9.3, v5.0.0 and v5.4.0.
- OCI runtime spec, lifecycle hooks (`createRuntime`, `prestart`).
