# Stack Secrets — Design

Status: partial — phases 1 and 2 (rotation, agent restart, host reboot) implemented and tested end to end on rootless Podman 4.9.3/runc; production layout, stack deletion, crun and Ubuntu 26.04 outstanding
Owner: Bart
Last updated: 2026-09-25

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
- [x] **Phase 1 — master secret store.** `internal/secretstore`: Argon2id (t=3, 64 MiB, p=4) key from the passphrase, AES-256-GCM per value with host/stack/name bound as associated data, one sealed file per secret under `<state_dir>/secrets`. Unlock, lock and initialize over `/api/secrets/*`; values are write-only (`PUT`/`DELETE /api/hosts/{h}/stacks/{s}/secrets/{name}`, `GET` lists names, sizes and times even while locked). Limits: 64 KiB per value, 64 secrets and 1 MiB per stack. The passphrase is entered in the web UI after start rather than on the server's terminal (see [Implementation notes](#implementation-notes-2026-09-24)).
- [x] **Phase 1 — swap warning.** The agent reports `swap_active` from `/proc/swaps` in its hello; the host list and host page show a warning. Whether swap is encrypted is not detected.
- [x] **Phase 1 — relay delivery.** Protocol version 4: `secrets.sync` (one stack's full set; the agent answers with changed/removed/restarted names and problems) and `secrets.retain` (forget stacks not listed, sent only after a fully successful push). The master pushes on value change, on unlock and on every agent connect. Values are never logged; the agent's vault is memory only and survives relay reconnects. Zeroing is best effort (JSON decoding makes copies Go cannot wipe).
- [x] **Phase 1 — agent hook.** `sorry-portainer-agent oci-hook --host-id H --socket S`, stage `createRuntime`, 15 s timeout, matched on annotation `io.sorry-portainer.agent=^H$`. The socket is `$XDG_RUNTIME_DIR/sorry-portainer/<host>.sock` (directory 0700, socket 0600, peer uid checked with `SO_PEERCRED`). The hook refuses unless `<rootfs>/run/secrets` is a tmpfs that is its own mount, creates files with `O_EXCL|O_NOFOLLOW` under `openat2` no-symlink resolution, and maps owner IDs through the container's user namespace. Any failure exits non-zero and runc refuses to start the container.
- [x] **Phase 1 — setup integration.** `setup` runs `setup-hooks-conf` as the agent user, which writes the drop-in `~/.config/containers/containers.conf.d/50-sorry-portainer.conf` (`hooks_dir` = Podman's defaults plus the agent's hooks directory). The agent writes its own hook JSON at startup. Development agents get the same drop-in content through `CONTAINERS_CONF_OVERRIDE` (`just install-dev-services`), so a developer's `~/.config/containers` is never touched.
- [x] **Phase 1 — compose rewrite.** `internal/stacks/compose.go`: standard compose `secrets:` (top-level entries empty or `external: true`; `file:`/`environment:` rejected; long syntax with `target`, `uid`, `gid`, `mode`). Services get the three `io.sorry-portainer.*` annotations and a `/run/secrets` tmpfs; the result goes to `runtime-compose.yaml` and the Podman-native `secrets:` never reaches `podman-compose`. Stacks may not use the `io.sorry-portainer.` prefix anywhere; merge keys and aliases that would bring in secrets, annotations, tmpfs or volumes are rejected. After `up` the agent checks every container really has its files and stops any that don't.
- [x] **Phase 1 — label `env.json` as non-secret.** The stack editor's secrets panel says the Compose file, `environment:` entries and `env.json` are plain text on the host; `protocol.StackSave.Environment` says so in the API.
- [x] **Phase 1 — UI.** Store banner (create, unlock, lock), per-stack secrets panel on the edit page (names, sizes, set/replace/delete, delivery state, Compose-referenced names that have no value yet), swap warnings. State lives in a Solid store (`src/secrets-store.ts`).
- [x] **Phase 2 — rotation.** A new value is pushed at once; the agent restarts running containers of the services that use a changed secret and checks the new files are in place. Removing a secret does not restart anything.
- [x] **Tests.** Unit tests for the store (including no plaintext on disk and tampering), vault, socket, hook (end to end in a user namespace), compose rewrite, stack flow, relay and server routes; a live rootless Podman test (`SORRY_PORTAINER_TEST_PODMAN=1 go test ./internal/stacks -run TestRealPodmanStackSecrets`) that covers up, owner/mode, rotation, a restart by Podman's restart policy, refusal without values, and a scan of Podman storage and `inspect` for the canary values.
- [x] **Fix — annotation encoding (extended beyond the original plan).** `podman run --annotation` parses its value as CSV, so the mount list is `source:target:uid:gid:mode;…` rather than JSON. Found by the live test.
- [x] **Decision — agent restart restarts every secret container.** Bart (2026-09-25): option (a), a digest in the tmpfs. A freshly started agent's vault is empty, so the reconnect push reports every value as changed. The hook now also writes `/run/secrets/.sorry-portainer-digest` (SHA-256 over each file's source, target, owner, mode and value, length-prefixed; mode 0400, owned by the container's root). On a push the agent reads it through `/proc/<pid>/root` and restarts only containers whose digest differs from the vault's, or that have none. See [Follow-up](#follow-up-2026-09-25-agent-restart-and-reboot).
- [x] **Decision — agent stop kills stack containers (not secret-specific).** Bart (2026-09-25): option (a). The agent unsets `INVOCATION_ID` at startup, before it runs any Podman command, so each conmon gets its own `libpod-conmon-<id>.scope`. Verified live: with the variable set, stopping the calling unit kills conmon and the container is stuck in "stopping"; without it, conmon survives and `podman stop` works. The dev agent restarted with `secret-demo` running: the container kept running and was not restarted.
- [x] **Phase 2 — host reboot and agent restart.** Bart (2026-09-25): the agent remembers. `Up` and `Down` record the desired state in `<stack>/state.json` (no secrets), with the host's boot ID when an `up` succeeded. At start the agent brings up, one at a time, every stack that should be up but has not been up since this boot; a stack whose secrets have not arrived waits, and the master's push starts it (`SecretSyncResult.started`). The stack list shows "starts after reboot" / "stays stopped after reboot" and why a stack is waiting; the list got a Stop button (with confirmation) so the desired state can be set to down from the UI.
- [x] **Tests — restart and reboot (extended beyond the original plan).** Unit tests for desired state, resume after reboot vs agent restart, waiting for and starting on the push, a broken state file, and digest comparisons (`internal/stacks/desired_test.go`, `internal/secrets`); benchmarks for listing and resuming 1000 stacks; the live Podman test now also covers an agent restart (push restarts nothing) and a simulated reboot (plain stack back at once, secret stack started by the push).

### Outstanding

- [ ] **Decision needed — one-off services after a reboot (not secret-specific).** Bringing a stack back runs `up`, which starts every stopped container of the stack, including one-off services that had exited on purpose (for example an init container that fixes volume permissions). Docker after a reboot only restarts containers with a restart policy. Options: (a) keep it: everything in the stack starts, as after a manual Start; (b) after a reboot, only start containers whose `restart:` policy is `always`, `unless-stopped` or `on-failure`, like Docker; (c) (a), plus a per-service opt-out label.
- [ ] **Production agent layout.** The default hooks directory is `~/.local/share/sorry-portainer/oci-hooks` while `state_dir` is configured separately; settle where each lives for the dedicated agent user.
- [ ] **Stack deletion.** No stack delete exists yet; when it does, it must delete the stack's secrets on the master and tell the agent to forget them.
- [ ] **Verification — crun.** Tested on runc only; Ubuntu hosts may use crun, whose hook-time paths could differ.
- [ ] **Verification — Ubuntu 26.04 / Podman 5.x and the dedicated agent user.** Re-run `experiments/stack-secrets/` and the live test there.
- [ ] **Defer — encrypted-swap detection.** The warning shows for any active swap.
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

## Implementation notes (2026-09-24)

The build follows the mechanism above, with these differences. The text above
is left as it was planned.

- **Unlock in the web UI.** "Passphrase typed at server start" became: the
  server starts locked and an admin enters the passphrase in the web UI (or
  `POST /api/secrets/unlock`). Nothing usable is on disk either way, and the
  server can run under systemd without a terminal. Initializing takes a
  passphrase of at least 12 characters and cannot be undone or recovered.
- **What the hook trusts.** Step 3 planned to look up allowed names from the
  compose project and service labels rather than the annotation. The agent
  instead trusts the `io.sorry-portainer.*` annotations, because it writes
  them itself and rejects any stack that uses that prefix anywhere in its
  Compose file. The socket answers only the agent's own uid, and only for the
  stack named in the annotation.
- **tmpfs check.** Instead of parsing `mountinfo`, the hook opens
  `<rootfs>/run/secrets` without following symlinks and checks with `fstatfs`
  that it is a tmpfs, and with `st_dev` that it is its own mount (not just a
  directory inside some other tmpfs).
- **Modes.** The tmpfs is `mode=0755` so non-root container users can reach
  their files; files default to 0444 like Docker Compose, and `uid`, `gid`
  and `mode` from the long syntax are honoured.
- **Where config lives.** Setup writes a `containers.conf.d` drop-in rather
  than editing `containers.conf`, and the agent writes the hook JSON itself at
  every start (so the binary path is always current). The agent does not check
  `hooks_dir` at startup; it checks after every `up` and rotation that each
  container has its files, and stops containers that don't, with a hint about
  `hooks_dir`.
- **Known limits.** A container that bind-mounts the agent's socket directory
  could ask for its stack's secrets; stacks are written by the admin, so this
  is out of scope. Merge keys and aliases are restricted in stacks that use
  secrets. `podman-compose` 1.0.6 `down` leaves the project network behind
  (not secret-specific; in `TODO.md`).

## Follow-up (2026-09-25): agent restart and reboot

Bart chose (a) for both open decisions and "the agent remembers" for reboots.

- **Telling a reboot from an agent restart.** The agent reads
  `/proc/sys/kernel/random/boot_id` at start. A stack whose `state.json` says
  `up` with an older boot ID has not come up since the host booted. In the
  same boot the containers are still running (conmon no longer dies with the
  agent), so nothing is started again.
- **Bring-up.** `Resume` runs in the background, one stack at a time, and
  uses the normal `up` path, so the secret check runs too. `podman-compose`
  1.0.6 `up -d` on existing stopped containers fails `podman run` (name in
  use) and then runs `podman start`, which is what brings them back. A stack
  missing secrets gets the reason "waiting for secrets … from the master";
  the push that completes its set runs `up` while holding the stack lock. A
  failed `up` still records `up` as desired and shows "start failed: …".
- **Digest.** The digest discloses nothing new: only the container's root can
  read it, and that user can read the values themselves. Its name starts
  with a dot, which a secret target cannot. In a nested user namespace
  (`--userns=auto`) the agent cannot read a file owned by the container's
  root, so such containers are restarted on every agent restart, as before.
- **Cost.** An agent restart reads one small state file per stack: 1000
  stacks in about 5 ms (`BenchmarkResumeAfterAgentRestart1000`); listing
  1000 started stacks takes about 7–9 ms. After a reboot the agent runs one
  `podman-compose up` per stack that should be up, one after another, so a
  busy host doesn't get every stack starting at once.
- **Known limits.** A stack started before this change has no `state.json`
  and stays stopped after a reboot until it is started once. If the master
  itself rebooted, it is locked until someone unlocks it, and secret stacks
  on every host wait until then.

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
