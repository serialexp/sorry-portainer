# Current task: move jp1 from Docker to Podman 6 — phases 1 and 2 done; next, phase 3 (the big-bang switch)

## Podman 6 packages and the jp1 move (2026-11-06)

- Plan (docs/design/caddy-podman.md): Caddy runs as a stack under the agent user, behind `{{upstreams}}` labels; big-bang switch; accounts become `sorry-portainer-master` and `sorry-portainer-agent` (hel1 renamed in place). Client addresses need Podman 6's `rootless_port_forwarder = "pasta"`, so we build Podman 6 ourselves.
- `~/Projects/podman-ubuntu` (git, pushed to gothab.io `serialexp/podman-ubuntu`, public; releases `v6.1.4-1` and `v6.1.4-2`, 20 .debs + 2 SHA256SUMS each; `v6.1.4-2` pinned in `deploy/rsansible/vars/podman.yml`, which the agent role installs): ten packages for 24.04 and 26.04, versions `<upstream>-1000sp<REV>~ubuntu<rel>` (above Ubuntu's: 26.04's catatonit is `0.2.1-2build1`). Includes `containers-storage` (storage.conf only), because Podman 6 applies a system storage.conf's graphroot to rootless users, and 26.04's sets one. Local builds pass for both releases; `smoke.sh fresh|upgrade` passes up to its rootless pasta check, which fails locally only because this workstation's old passt AppArmor profile confines the new pasta. On Gothab CI (no such profile) all four combinations pass, including the rootless check: with a client in its own netns, rootlessport shows 10.89.0.2 and pasta the client's 192.0.2.2.
- Gothab apt registry: draft requirements REQ-53 to REQ-61 on gothab.io, implementation record `~/Projects/gothab/docs/design/apt-registry.md` (committed, not pushed). For another agent; until it lands, hosts pin release assets (`scripts/pins.sh <tag>`).
- jp1 (2026-11-06): `v6.1.4-1` installed over Ubuntu's 4.9.3 (Docker untouched). It had two AppArmor bugs CI couldn't see: upstream installs pasta as a symlink (so it ran under passt's profile), and pasta's profile denies podman's SIGTERM on teardown. Both fixed by hand on jp1 (hard links; patched `/etc/apparmor.d/usr.bin.pasta`, identical to revision 2's) and in podman-ubuntu `758ed78` (REVISION=2, `patches/passt/`). CI now loads the profiles (`SMOKE_APPARMOR=1`) and passes. Experiment (user removed afterwards): real client address through caddy-docker-proxy labels; host loopback works with `pasta_options = ["--map-host-loopback", "169.254.1.3"]` (dashboard at 169.254.1.3:6200). Release `v6.1.4-2` published and installed on jp1 (pins in `deploy/rsansible/vars/podman.yml`). Bart chose host-loopback mapping for the dashboard route.
- **Wheat:** `wheat add docs/design/caddy-podman.md` failed with `cannot normalize ...: VerificationFailed` earlier on 2026-11-06. Later the same day it captured fine, with no workaround, as part of the roles change; cause unknown.
- **Phase 1 in code (2026-11-06), two Wheat changes, not released:**
  - "Agent setup: Podman 6 port forwarding, host loopback and the sorry-portainer-agent account" (`01a1b141…`). `DefaultUser` is `sorry-portainer-agent`. `setup` installs whichever of podman, podman-compose, crun, netavark, aardvark-dns and passt is missing. The home comes from passwd. `setup-hooks-conf` became `setup-containers-conf`, which writes hooks_dir, pasta forwarding on Podman ≥6 and `--map-host-loopback ADDR` on ≥5. It refuses a forwarder change while containers run and has a `--check` mode, which `setup --dry-run` uses as root. `go test ./...` and the real-Podman secrets test pass.
  - "Roles: Podman 6 packages, account rename to sorry-portainer-master/-agent" (`01a1b145…`). Agent role: `rename.yml` (old `sorry-portainer` in /home → `-agent`; refuses non-empty storage, resets it, moves home, renames group and subuid/subgid) and `podman.yml` (pinned debs, compared with dpkg-query). New variable `sorry_portainer_agent_host_loopback`. Server role: `rename.yml` (old `sorry-portainer` with the state dir as its home → `-master`) and the new account everywhere. `rsansible validate` passes. podman-ubuntu `b9c2b04` adds `version` to the pins (not pushed).
  - Released as v0.2.3 (pinned in `vars/release.yml`) and applied on 2026-11-06: jp1's master is `sorry-portainer-master`, and hel1 is `sorry-portainer-agent` on Podman 6.1.4 and online. A second run changes nothing. Role fixes found on the way: no set_fact for "26.04", and lineinfile backrefs `${1}`. hel1's subuid/subgid were repaired by hand; the backups `/etc/sub[ug]id.sp-backup` are still there, so ask before deleting them. Both rsansible quirks are noted in rsansible's TODO.md. podman-ubuntu `b9c2b04` is pushed.
- **rsansible (2026-11-06/07):** `1fa7030` (pushed, installed) makes a value that is exactly one `{{ expr }}` keep the expression's type (Ansible 2.19), and anything else a string. set_fact no longer turns "26.04" into a number; the comment about that in `podman.yml` is gone (own Wheat change). An audit of gothab, gothab2, rsansible examples and this repo found nothing that breaks.
- **Phase 2 done (2026-11-06):** jp1 is in `sorry_portainer_agents` with `sorry_portainer_agent_host_loopback: 169.254.1.3` (local inventory). Agent `sorry-portainer-agent` on jp1 is enrolled and online; containers reach the host's 127.0.0.1 at 169.254.1.3; a second run changes nothing. Role fixes for check mode on a new host (Wheat change "Roles: check mode on a host without the agent"): the setup dry run waits for the downloaded binary, and enrollment waits for the agent account (rsansible has no `ansible_check_mode`; noted in its TODO.md).
- **Phase 3 decisions (2026-11-07):** credentials go to each stack's env.json (inline values become `${VAR}`, extracted on jp1 without printing them); every volume moves in place with a uid shift; stacks are imported with a new CLI (Bart: "SSH to admin socket"). Survey and decisions are in caddy-podman.md (Wheat changes "phase 3 survey" and "phase 3 decisions"). Scratch survey tooling is in `/tmp/jp1-survey/`, outside the repo.
- **Admin CLI (docs/design/admin-cli.md), phase 1 done (2026-11-07), not released.** Wheat change "Remote CLI: stack actions on the admin socket, reached over SSH" adds:
  - `stacks.*` actions on the admin socket;
  - `sorry-portainer-server admin dial-stdio`;
  - `cmd/sorry-portainer` (`--master ssh://root@HOST`, `--host` everywhere, `--env-file`, `--json`, exit codes 0/1/2);
  - a relay fix: failed Compose runs keep their output.

  `go test -race -count=1 ./...` and `go vet` pass.
- **Admin CLI phase 2 done (2026-11-07):** v0.2.4 released (run 7) and pinned in `vars/release.yml`; deployed to jp1 and hel1, and a second run changes nothing. `/usr/local/bin/sorry-portainer` is on jp1. Working from here: `sorry-portainer --master ssh://root@jp1.serial-experiments.com hosts list`, and `stacks list` for both hosts. Create, up, down and restart haven't been run for real yet, since stacks can't be deleted and a test stack would stay behind.
- Next: phase 3 prep:
  - a converter on jp1 (env extraction without printing, fully qualified images, no `container_name`);
  - the Caddy stack;
  - a volume move-with-uid-shift script;
  - a dry run;
  - then the switch window with Bart. Still open: the CLAUDE.md native-packages rule, and deleting hel1's `/etc/sub[ug]id.sp-backup`.
- Gothab follow-up noted in its TODO.md: a `namespace:repos:create` scope (creating a repo needed `namespace:settings:write`, which also allows deleting the namespace).

## Deployment (2026-11-05)

- `deploy/rsansible`: `site.yml`, roles `sorry-portainer-server` and `sorry-portainer-agent`, `vars/release.yml` (pins), `inventory.example.yml`, `README.md`. The real inventory `deploy/rsansible/inventory/` is Wheat-ignored. The admin password was generated, unseen, into `inventory/group_vars/all/secrets.yml`; Bart reads it from there.
- Releases: v0.2.1 adds `init --admin-password-file`. v0.2.2 fixes the agent unit (`%U` meant root in a system unit, so the agent crash-looped on `/run/user/0`). Both are on Gothab only; GitHub was not pushed (ask first).
- Live: the master is on jp1. Control is on `:9443`, publicly reachable. The dashboard listens on `172.20.0.1:6200` (the caddy bridge) and is served at https://sorry-portainer.jp.serial-experiments.com through the `sorry-portainer-caddy` label container. hel1 runs Podman 5.7.0 and podman-compose 1.5.0, is enrolled, and shows online. A second run changes nothing.
- rsansible gaps worked around: `become_user` isn't templated (the account is fixed as `sorry-portainer`); there are no `dirname` or `tojson` filters (`to_json` is compact); `stat` has no `checksum_algorithm` (it is always sha256).
- Not checked: signing in with the real password (it stays unread, Rule 16), and a reboot of either host.


## Release v0.2.0 (2026-11-05)

- Gothab is the primary repository (not a mirror of GitHub). Main and tag `v0.2.0` (commit a2092f14) were pushed to Gothab with `wheat push ssh://git@gothab.io/serialexp/sorry-portainer.git --to refs/heads/main` / `--tag v0.2.0`, and also to GitHub before Bart pointed out Gothab is primary (same commit IDs).
- Release run af9c36ca passed; https://gothab.io/serialexp/sorry-portainer/releases/4396940f-cc55-4ea1-bd4f-69812a164288 has both linux-amd64 binaries and SHA256SUMS (agent 167b6f34…, server 63324cf2…). Assets download anonymously (redirect to signed R2 URLs).
- Before publishing, test fixtures naming jp1's real hostname/IP were replaced with reserved example values (own Wheat change).

## Review fixes (2026-11-05)

Wheat change: "Fix review findings before v0.2.0". Codex and Antigravity reviewed; Bart chose to fix 1–6 now, the rest is in TODO.md.

1. Web body reads bounded: `boundBodyRead` (ResponseController read deadline, `bodyReadTimeout` 30 s) in `jsonDecode` and the token decoder. net/http clears the deadline itself when the body hits EOF (`startBackgroundRead`), so long handlers are not cancelled; `internal/server/body_test.go` covers both (mutation-checked).
2. `registry.update` wraps `errPublished` when only the directory sync failed; `Remove` and `Enroll` then still disconnect the host (test + mutation check).
3. Admin socket: `MaxRequest` 64 KiB, `MaxResponse` 16 MiB; test with a 10,000-host list.
4. Stack editor: a stack it created keeps its name (read-only) and later saves update it; route loads use a request counter (also fixes A→B→A). Fixed the name `pattern`, which the `v` regex flag rejected.
5. Stack and secret APIs map 401 to `UnauthorizedError`; callers go through `sessionEnded`.
6. `onSessionEnd` in fleet-store; enrollment and secrets stores reset on session end with generation counters, so a late token answer is dropped; leaving the enrollment page calls `resetEnrollment`; `loadHosts`/`loadEnrollment` have request counters.

Verified: `go test -race ./...`, 30 node tests, tsc, biome format, build; browser: stack 401 → login, issued token gone after sign-out, created stack's name locked. The test stack `review-rename` remains in local-a's dev state (no stack deletion yet).

## Session hardening (2026-11-05)

Wheat change: "Harden dashboard sessions". Bart's choices: Secure cookie by default with `insecure_cookie` for plain-HTTP development; per-client sign-in limit keyed through `trusted_proxies`.

- Code: `internal/server/session.go` (`SessionOptions`, `Logout`, bounded table), `internal/server/loginlimit.go` (GCRA limiter, `clientKey`), config fields `insecure_cookie`/`trusted_proxies` (+ env `SORRY_PORTAINER_INSECURE_COOKIE=1`, `SORRY_PORTAINER_TRUSTED_PROXIES`), `init --insecure-cookie`, schema. Frontend: `logout()`, `MasterUnreachableError` (demo only when unreachable), sign-out calls the server.
- `just dev-init` adds `insecure_cookie: true` to existing dev state once; done on this machine.
- Tests: session/limiter tests, five mutation checks (refund, untrusted XFF, logout delete, session eviction, Secure flag), benchmarks `go test -run '^$' -bench 'LimiterFullTable|LoginAtSessionCap' ./internal/server` (22 ns/0 allocs; 37 µs). Live: 10×401 then 429 Retry-After 3; logout clears the cookie; reload after sign-out stays signed out.
- For the role: on jp1 `listen_addr` must be the Docker bridge gateway (what `host.docker.internal` resolves to), and `trusted_proxies` the Caddy network's subnet (TODO.md).
- Next: Bart asked for a Codex and Antigravity review of the code, then release (ask first), then the role.

## Dashboard store and enrollment page (2026-11-05)

Wheat changes, in order: "Enrollment page and web API" (`01a1a75d…`, the `/api/enrollment/` routes), "Dashboard state in a Solid store" (`01a1a767…`), "Enrollment page in the dashboard" (`01a1a76d…`). The page could not be added to `01a1a75d…` because it builds on the store change; Wheat refused without `--force`, which was not used.

- Bart's decisions: UI first, then release; dashboard on jp1 through a label-only container on the caddy network that proxies to the host's 127.0.0.1:8080 (caddy needs `host-gateway`); control port 9443 public; refactor the dashboard state into stores before the page.
- Store: `src/fleet-api.ts` (session, hosts, containers; `UnauthorizedError`), `src/fleet-store.ts` (phase booting/login/ready, demo flag, selected host and its resources, search selectors, `sessionEnded`). `index.tsx` components and `StackList`/`StackEditor` take no data props; the editor reads the stack name from the URL. Behaviour fixes on the way: a wrong password shows "Wrong password." instead of opening demo mode (demo mode only when sign-in cannot reach the master); container start/stop reports failures and reloads the list instead of faking the result; a 401 anywhere returns to the login form.
- Enrollment page: `/enrollment` (sidebar and overview links); `src/enrollment-api.ts` (`certificateState`, overdue below 30 days), `enrollment-store.ts`, `enrollment-ui.tsx`. Token answer has `reenroll`; the command then ends in `--replace` and the page says to restart the agent.
- Tests: `node --test tests/*.mjs` 26 pass; `tsc`, `biome format`, `pnpm build` clean; `go test -race ./...` green. Browser check on the dev UI: wrong/right password, reload keeps the session, host switch, images, stack editor, sign out, token for a new and an enrolled host, revoke. Remove was not clicked on the dev hosts (it would de-enroll them); the Go route test covers it.
- Dev: the Vite unit had been running since Sep 25 from the old `sorry-portainer.old-wheat` directory; restarted. The dev server was rebuilt and restarted.
- Next: done — see "Session hardening" above.

## Agent enrollment, allowlisting and rotation (2026-11-05)

Wheat change: "Agent enrollment with one-time tokens and host allowlisting" (`01a1a724…`). Design, checklist, review findings and measured performance: `docs/design/agent-enrollment.md`. Phase 1 is implemented; Phase 2 (web UI, rsansible role) is outstanding.

- Bart's decisions: tokens over the network (option B); 90-day agent and server certificates with automatic renewal; tokens in memory only; agent layout `/var/lib/sorry-portainer-agent` (owned by the agent user, 0700); role in this repo with the inventory Wheat-ignored; master on jp1; binaries from the Gothab release workflow (needs a new tag, e.g. v0.2.0, since this adds code — confirm with Bart before publishing).
- Packages: `internal/pki` (CA, issuance, CSR, bundles, pinning, `Fingerprint.Matches`), `internal/atomicfile` (durable write, `LockDir`), `internal/enrollment` (registry `hosts.json`, tokens, service), `internal/controlcert` (server certificate self-renewal via `GetCertificate`), `internal/agentcred` (holder, enroll, renew), `internal/admin` (admin socket server and client), `internal/unixsock` (shared with the secret socket), `internal/provision` (`Init`, `ResetPKI`), `internal/server/control.go` + `listeners.go`, `internal/relay` (`Serve` admit hook, `Disconnect`, agent drain + `RequestReconnect`), `internal/testcontrol` (test master).
- Commands: `sorry-portainer-server init --server-name …` (repeatable), `pki reset`, `hosts token|list [--json]|remove`; `sorry-portainer-agent enroll --server-url … --host-id … --dir … --token-file -|PATH [--prefix --server-name --compose-provider --replace]`; `setup` creates the agent dir and prints the enroll step. `server agent --create` is gone.
- Dev: `just dev-init` migrated `.dev/state` (old PKI in `.dev/state/pki-retired-20261104T160639Z/`), tightened it to 0700, and enrolled local-a/b/c; a second run is a no-op. All three online with stacks intact.
- Tests: `GOFLAGS=-buildvcs=false go test -race ./...` green; benchmarks `go test -run '^$' -bench . ./internal/enrollment ./internal/integration`; six mutation checks recorded in the design doc.
- Next: tag and release (ask Bart first), then the rsansible role in `deploy/rsansible` (inventory Wheat-ignored; look at `~/Projects/gothab/ansible/roles` and `~/Projects/rsansible` examples/`RSANSIBLE_IDIOMS.md`; invoke `~/.local/bin/rsansible`). The server CLI's `pki reset` and `init` must run as the server's account (they refuse a state dir owned by someone else); the role should use `become_user` for them and for `enroll` on agents.
- Open questions for Bart before the role: jp1 disk (94% full; reclaimable listed in the conversation, nothing deleted) and the one-off-services-after-reboot decision. (Web UI routing and the control port were answered; see above.)

## Agent restart and reboot (2026-09-25)

Wheat change: "Survive agent restarts and host reboots". Bart's choices: clear `INVOCATION_ID`; digest in the tmpfs; the agent remembers desired state. Details: `docs/design/stack-secrets.md` "Follow-up (2026-09-25)".

- Agent: `cmd/sorry-portainer-agent/main.go` (`detachFromServiceUnit`, `readBootID`, `SetBootID`, `go manager.Resume(ctx)`).
- Digest: `internal/secrets/digest.go` (`Digest`, `ReadDigest`, `DigestFile`), written by the hook (`hook.go` `inject`), `Vault.Digest`.
- Stacks: `internal/stacks/desired.go` (`state.json`, pending/waiting, `Resume`), `stacks.go` (`operateLocked` records desired state; `List`/`Inspect` fill `desired`/`waiting`), `secrets.go` (`restartOutdated` compares digests, `bringUpIfWaiting`).
- Protocol: `Stack.Desired`, `Stack.Waiting`, `SecretSyncResult.Started` (additive, still v4).
- UI: stack list shows "starts after reboot" / "stays stopped after reboot" and the waiting reason; new Stop button (confirm); delivery line mentions a start.
- Tests: `go test -race ./...`, `node --test tests/*.mjs` (15), live `SORRY_PORTAINER_TEST_PODMAN=1 ... -run TestRealPodman ./internal/stacks/` (3× green, ~72 s), mutation checks on the new tests, benchmarks at 1000 stacks.
- Dev check: restarted `sorry-portainer-agent-a` with `secret-demo` running: container kept running, not restarted, conmon in its own scope, push delivered 3 s later.
- Open: one-off services start again after a reboot (Docker wouldn't); decision item in the design doc and TODO.md.

### Deploy context (hel1 / jp1)

- Bart wants sorry-portainer on the two hosts that run Portainer today (hel1, jp1). The master goes on jp1; Bart is upgrading jp1's Ubuntu first (Podman 4.7+ needs 24.04 or later).
- Both hosts run their apps on Docker behind `caddy-docker-proxy`, which only sees Docker containers. Moving to rootless Podman means copying volumes and a new way to route web traffic to Podman apps.
- The host inventory (OS, apps, access) was gathered over SSH on 2026-09-25 and is deliberately not recorded here: this repository is pushed to a public GitHub repo. Inspect the hosts again before migrating.
- Remaining gaps before production: production agent layout, image pull/redeploy, logs, stack deletion, certificate allowlist, Ubuntu 26.04/dedicated-user verification.

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
