# Current task: move jp1 from Docker to Podman 6 — phases 1–3 done (switched 2026-11-08); next, the reboot experiment and phase 4. v0.2.10 (image update check and Update per service) is released and deployed to jp1 and hel1 (2026-11-12).

## Registry logins (rapid-owl, 2026-11-12), phases 1 and 2 done, not released

- Bart: private Docker Hub images can't be checked or pulled; the agent account on jp1/hel1 has no login files. Design `docs/design/registry-logins.md`, all decisions made by Bart (master's secret store, dashboard page, all hosts, push and keep with a per-host tmpfs auth.json, show after passphrase, test via an agent, registry or namespace keys, username in the clear, reuse the tmpfs file on agent restart, agent pre-pulls before `up`, corrupt file → start empty). Reviewed by Codex and Antigravity; findings applied (see its Review section). Design board: `project/RegistryLogins.dc.html` in the design artifact; Bart's placement notes are in the doc's follow-up section.
- Phase 1 (backend) is captured in Wheat: "Registry logins: agent vault, pre-pull and relay operations", "Registry logins: master store, background pusher and /api/registries", "Relay: report a requested reconnect even when the server's close arrives first", "Registry logins: live Podman test against a private TLS registry". The race suite passes; the live test (`SORRY_PORTAINER_TEST_PODMAN=1 go test -run TestRealPodmanPrivateRegistry ./internal/stacks/`) passes against real Podman and podman-compose. The doc's checklist is ticked (Mode B).
- Open for Bart: encoding a round's set once instead of per host (benchmark numbers in the doc); what the stacks list says for a stack waiting on a registry with no saved login (`waiting_login_state: none`), which the board doesn't show.
- Phase 2 (UI) is captured in Wheat: "Registry logins: Settings page and login states on stack pages". 111 frontend tests, tsc and the build pass. Checked in the browser against a throwaway unlocked master (state was in `/run/user/1000/sp-ui-scratch`, now removed): add, duplicate key, scope hint (trailing slashes dropped), Show with wrong/right passphrase, Replace with an empty password, lock/unlock, Delete, restart → locked read-only list. The stack-page login states are unit-tested only (the dev store is locked).
- Open for Bart: the board gaps listed in the doc's Outstanding section (interim choices made), and the encoding option.
- Next: ask before releasing v0.2.11 (server and agents deploy together; protocol version 5).


- Wheat changes "Stack image updates: registry check per service, Update (pull + force-recreate)", "Image checks: tell a refused or missing tag apart from an unreachable registry" and "Stack updates report their steps: relay progress frames, streamed to the dashboard" (design: `docs/design/image-updates.md`). Open: the design agent's wording for pinned/local/unresolved images and the failure states.
- v0.2.10: main and tag → `7aae00a` pushed to Gothab as separate steps; release run `8504d451…` passed, release `4d85a5f1-6078-411f-be52-957ae5458e5d`; assets checked against SHA256SUMS and pinned in `vars/release.yml`. rsansible rebuilt from source; check run, real run (jp1 server, CLI and agent; hel1 agent), second run changed=0. `hosts list` shows jp1 and hel1 online; no errors in the logs. Server and agents must be deployed together (a new agent's progress frames disconnect an old server). GitHub not pushed.

## Dashboard per the design, v0.2.9 (rapid-owl, 2026-11-10)

- Wheat changes "Dashboard: sidebar, header and stacks list per the design, with Phosphor icons", "Stacks list: pill problems, row notes and output, Refresh, start/stop rules, empty state" and "UI: breadcrumb page header for host pages, Stack/Image/Volume/New/Edit stack detail headers" implement the design agent's seven answers on the Stacks board. Leftovers (Stack detail body, Image "Pull again", Volumes loading flash) are in TODO.md.
- **v0.2.8 on Gothab is a mis-build: do not pin or deploy it.** Gothab's main was at `2c58563`, the first revision of "Release v0.2.7: pin and deploy", which Wheat no longer projects (CURRENT_TASK.md was added to that change afterwards), so `wheat push --to refs/heads/main` refused. A chained command hid that failure and dispatched the release workflow anyway; Gothab checked out main and `gt release create` made the tag itself, so release v0.2.8 (`9eaa18f4…`) and tag `v0.2.8` → `2c58563` hold v0.2.7's code. Bart chose to leave it and release as v0.2.9. Main was then replaced with Bart's approval (`--force-with-lease` on `2c58563`, now `8dbbf29`). Lesson: run push, tag push and dispatch as separate steps and check each exit code; never add to a change that is already pushed.
- v0.2.9: tag → `8dbbf29`, release run `8d2d1a49…` passed, release `c7d0b7dd-cfad-485c-aa25-22f1a068be68`; assets checked against SHA256SUMS and pinned in `vars/release.yml`. The agent and CLI binaries are byte-identical to v0.2.7 (only the embedded dashboard changed). rsansible rebuilt from source; check run, real run (jp1: server binary and restart only), second run changed=0. `hosts list` shows jp1 and hel1 online. GitHub not pushed.
- Dev local-a: stacks `env-demo` and `vol-demo` (with volume `sorry-local-a-vol-demo_data`) are running; `review-rename` was started and failed (Docker Hub pull).

## Secret store UI per the design (rapid-owl, 2026-11-09)

- Design source: the Claude Design canvas recorded in CLAUDE.md ("Design source"). The "Secret store" board is the spec; the design agent answered seven placement questions on it.
- Done and captured in Wheat ("Secret store UI: sidebar status, Hosts create card, stack page banners, attempt countdown"). Frontend tests (78), tsc and build pass. Checked in the dev browser against the real master: sidebar locked → expand → wrong passphrase (field keeps text) → Escape collapses; the 429 countdown disables the field and re-enables it; stack detail and edit banners; panel disabled while locked.
- Not checked in the browser: "not set up" (Hosts create card, sidebar Set up, stack page line) and "no secret store". The dev store would have to be parked to show them, and its passphrase isn't recorded. Unlocking itself wasn't checked for the same reason.
- Differences from the design left alone (not asked for): the StackDetail board puts the secrets table on the detail page with Name/Size/Used by/Updated columns and an "Add secret" button; ours keeps the panel on the edit page. New stack has no "What this creates" panel; the no-store note replaces the secrets note there instead.
- Earlier the same day: the "Connected to the Podman fleet" notice was removed (Wheat "Dashboard: drop the Connected notice…").

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
- **foundryvtt imported on jp1 (2026-11-07), not started.** `/tmp/jp1-survey/convert.py` (run on jp1 as root with `python3 -I`) moved the 9 inline values to `/root/sp-import/foundryvtt/stack.env` (0600, still there; ask before deleting) and qualified the image. `sorry-portainer stacks create` on jp1 made version 1; env.json matches. Docker's foundryvtt still serves; starting the Podman one waits for the volume move.
- **Editable environment (docs/design/stack-environment.md), released in v0.2.5 and deployed 2026-11-07** (second run changes nothing; jp1 returns foundryvtt's 9 keys from its old-format env.json). Bart: env values are configuration and must be visible and editable; a changed environment recreates a running stack. Per-revision `revisions/N.env.json` (the old stack-level `env.json` still applies to older revisions, so foundryvtt keeps working). Tests, mutation checks, the real-Podman test and a dev-dashboard check pass. Open: should compose-only saves also recreate a running stack? Dev stack `env-demo` stays on local-a, stopped.
- **Showing a stored stack secret (2026-11-07), not released.** Bart chose "Show, re-enter passphrase". Wheat change "Stack secrets: show a stored value after re-entering the passphrase":
  - `secretstore.Store.Reveal` (needs an unlocked store and the passphrase);
  - `POST /api/hosts/{h}/stacks/{s}/secrets/{name}/reveal`, which answers no-store and logs the name and client but never the value;
  - a per-client limiter for wrong passphrases, shared with `unlock`, which had no limit before;
  - a Show button and passphrase prompt in the secrets panel.

  The design follow-up is in `stack-secrets.md`. Go and frontend tests pass, and the dev dashboard was checked by hand. For that check the dev secret store was parked under `.dev/state/secrets.parked-20261107`, because its passphrase isn't recorded. It was restored afterwards, and the temporary store was removed. Not released (ask first).
- **Phase 3 tooling (2026-11-08), checked on jp1 with Docker up.** Details are in caddy-podman.md under "Phase 3 tooling".
  - Wheat change "Roles: loopback dashboard route and unprivileged port start for a Podman Caddy stack". jp1's inventory doesn't set either yet; they are set in the switch window.
  - Scratch scripts on jp1 in `/root/sp-import`, kept out of the repo by Bart's choice. Local copies, with their tests, are in this session's scratchpad `jp1/`:
    - `convert2.py`, which produced `v2/<stack>/`, plus `v2/caddy/`;
    - `copy_images.py`;
    - `move_volumes.py`. Its tests need root in a user namespace: `unshare --map-auto --map-root-user python3 -m unittest test_move_volumes`.
  - All check modes pass on jp1. `copy_images.py` needs `--allow-tag felddy/foundryvtt:release`, because foundryvtt runs an older image than the tag.
- **Phase 3 switch done (2026-11-08).** Every jp1 site runs on Podman under `sorry-portainer-agent`, behind the Caddy stack, and matches its pre-switch HTTP status. Details are in caddy-podman.md, "Phase 3 — big-bang switch".
  - Stack versions: foundryvtt v3 and foundryvtt2 v2 (pinned to `:10.291.0`/`:11.311.0`, no login variables); homes v2 (`default` network declared); the rest v1.
  - jp1's local inventory sets `sorry_portainer_dashboard_route: loopback` and `sorry_portainer_agent_unprivileged_port_start: 80`.
  - Docker still runs with the 20 stack containers stopped (`/root/sp-import/docker-stopped.txt`), the two foundry containers at `restart=no` (were `always`), and Portainer up but unrouted.
  - Rollback: Podman stacks down, `move_volumes.py rollback --all`, route back to `docker`, then start the Docker containers.
- **Container health and restart counts (2026-11-09), not released.** Bart chose "option 2": the agent parses Podman's health result into its own field. Wheat change "Agent and dashboard: container health and restart counts":
  - Protocol: `Container` and `StackService` gain `health` (healthy, unhealthy, starting, ...; empty without a health check) and `restarts` (restart-policy restarts). Both are `omitempty`, so older agents and masters interoperate (no protocol version bump). `Status` no longer carries the ` (unhealthy)` suffix.
  - Agent: `splitHealth` in `internal/podman/adapter.go` takes the trailing ` (word)` off Podman's status (≥4.7 appends it); `Restarts` comes from `podman ps` and the compose listing.
  - Dashboard: `HealthNote` (`src/health-note.tsx`) shows an Unhealthy warning chip, a muted Healthy/Starting note and "Restarted N×" on the host containers table and stack service rows. A stack whose running services include an unhealthy one is "Running, N unhealthy" in the warning tone (`degraded`). Restart counts don't change the stack's tone.
  - Also fixed: stack service rows showed Podman's raw lowercase state ("exited"); `serviceStateLabel` capitalizes it.
  - Go tests (race), 62 frontend tests, tsc and the build pass. Dev check on local-a with a throwaway `health-demo` stack (failing check, passing check, crash-looping service), now down and left saved. This workstation's user systemd refuses Podman's healthcheck timers (`/run/user/1000/systemd/private: connection refused`), so the checks were run by hand with `podman healthcheck run`; jp1's timers do run.
- **Recent restarts and recovered health checks (2026-11-09), not released.** Bart chose: both a recent-restart warning and a restart rate, a 15-minute window, a warning for a check that failed recently, and Podman events as the source (over in-memory sampling). Wheat change "Agent and dashboard: recent restarts and recovered health checks from Podman events":
  - Agent: after `podman ps`, one `podman events --stream=false --since <now-15m> --filter event=restart --filter event=health_status --format json` (`internal/podman/events.go`). Output is folded per container as it streams (`eventSink`, one line at most 256 KiB), so memory doesn't grow with the event count. Podman 4 writes `"Time"` as RFC 3339 and Podman 5+ `"time"`/`"timeNano"`; both are read. Restart events are capped at Podman's restart count, which a user start resets, so user restarts don't count. Skipped when the list is empty and for `ProjectContainers` (secret restarts). A failing events call fails the list, loudly.
  - Protocol: `protocol.Recent` (`recent_restarts`, `last_restart_ago_s`, `unhealthy_ago_s`) embedded in `Container` and `StackService`. Ages are seconds on the agent host's clock, so skew doesn't matter. Optional fields, so no version bump; `protocol.RecentWindow` is 15 minutes.
  - Dashboard (`src/container-health.ts`): warning chips "Restarted 4 min ago", "N restarts in 15 min" and "Was unhealthy 6 min ago" (details in the tooltip). A stack counts each service once under its most urgent reason (unhealthy, restarted, recovered): "Running, 1 unhealthy, 1 restarted, 1 recovered" is degraded; a crash loop caught between runs reads "Stopped, 1 restarted" in the warning tone. The service table's State column widened to 220px.
  - Cost: the events query took 0.14 s on jp1 (about 650 events in 15 minutes) and is paid on each container or stack list.
  - Tests: unit tests for both event formats, split writes, the line cap, malformed events and the restart cap (mutation-checked); a real-Podman test (`SORRY_PORTAINER_TEST_PODMAN=1`, `TestRealPodmanRecentActivity`) passes on 4.9.3. Podman 6's format is covered by unit tests modelled on jp1's real output. Found on the way: the sink's `Write` returned the unconsumed length, which exec treats as a short write; fixed and tested. Dev dashboard checked with `health-demo` v2 (now down).
- **v0.2.6 released and deployed (2026-11-09).** Bart: "release 2.6". Contains the secret reveal, container health, and recent restarts/recovered checks. Release run `9792af6d…` passed; release `9a04ca6b-a6d2-49cb-a49c-d32caeb8c451` on Gothab, assets checked against SHA256SUMS and pinned in `vars/release.yml`. Deployed to jp1 and hel1 with rsansible (rebuilt from source first): check run, real run, then a second run with changed=0. Both hosts online. On jp1, `stacks list --json` shows health from Podman 6.1.4: groovewhale `web` and homes `mysql` unhealthy (the known pre-existing checks), the rest healthy, homes `worker` with one older restart. GitHub not pushed.
- Next: the reboot experiment (ask Bart before rebooting jp1), then phase 4. Pre-existing unhealthy checks (groovewhale web, homes mysql) are in TODO.md.

  Still open: the CLAUDE.md native-packages rule, and deleting hel1's `/etc/sub[ug]id.sp-backup` and jp1's `/root/sp-import`.
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

## Sorry-portainer redesign: volumes and images (gentle-badger, 2026-11-08)

- Done and captured in Wheat: volumes (change 01a1b690-7d78-7a60-b0e4-c9a1e879d1f3, "Volumes: mounted-by list and volume page"). Volume list shows the containers that mount each volume; volume page shows mountpoint, created, labels and mounted-by. Agent op `volumes.get`, route `/api/hosts/{id}/volumes/{name}`.
- Built, tested, NOT captured: images (change 01a1b69a-9209-7610-ad88-4cdd63f6ab65 exists but is empty). Image list shows used-by containers; image page shows config, layers and users. Agent op `images.get`, route `/api/hosts/{id}/images/{hex id}`. Files overlap the volume change (adapter.go, protocol.go, relay/*, server.go, index.tsx, styles.css), so committing them needs a bundled change or Bart's decision (Rule 13).
- Not started: SecretStore states artboard (overlaps rapid-owl's secrets-ui work).
- Known open items: volume and image "Remove" buttons (no remove op built); volume "Size on disk" (podman 4.9 system df has no per-volume size); TestSecretStoreLifecycleOverHTTP in internal/server is intermittently failing (2 syncs instead of 1, about 1 run in 5), in rapid-owl's secrets code.

## Sorry-portainer redesign: volumes and images, remove and sizes (gentle-badger, 2026-11-08)

Done and captured in Wheat:

- Images and volumes, list and detail pages (change 01a1b69a-9209-7610-ad88-4cdd63f6ab65, revision 41f2481f…, which also holds the volume work that change 01a1b690 captured earlier, so the two overlap).
- Remove: `DELETE /api/hosts/{id}/volumes/{name}` runs `volume rm --` and `DELETE /api/hosts/{id}/images/{id}` runs `rmi --`, with no force. Podman's refusals (a volume a container mounts, an image a container uses, an image with several tags) come back as HTTP 409 with Podman's message. The UI disables Remove when this prefix's containers use the volume or image, and asks first with `window.confirm`, as the stack pages do.
- Volume size: a background scan in the agent (`internal/podman/volumesize.go`, started from `cmd/sorry-portainer-agent`) walks each prefixed volume's mountpoint every 15 minutes (`DefaultVolumeSizeInterval`), one volume at a time. Podman 4.9 has no per-volume size. Listings read the stored value and never wait for a walk. Unreadable entries are skipped and mark the size partial ("At least"). Symlinks are not followed. Hard links count once per link.
- Verified against real Podman on this machine: the scan measured 3,001,000 bytes for a known volume with no partial flag, and the remove worked. The temporary check file was deleted, and no test volumes are left.
- Benchmark `BenchmarkVolumeSizeScan` (20 volumes, 50,000 one-byte files): about 58 ms and 308k allocations per scan on this machine (Ryzen 9 9900X, warm cache). Most allocations come from `filepath.WalkDir` and `DirEntry.Info()`. A custom walker could cut them, at the cost of more code.

Not done:

- SecretStore states artboard: not started. It overlaps rapid-owl's secrets-ui work.
- `TestSecretStoreLifecycleOverHTTP` (internal/server/secrets_test.go, rapid-owl's code) failed once in about five runs earlier. It passed 8 of 8 in the latest run. Not investigated (Rule 0.5). Rerun before relying on it.

Open decisions for Bart:

- Image removal is not scoped to the prefix (images belong to the whole Podman install). The UI blocks only when this prefix's containers use the image, so other prefixes' use shows up as Podman's refusal.
- An image with several tags cannot be removed by ID without force. The UI shows Podman's message. Options: remove a single tag, or add a force option.
- Partial sizes: the UI shows "At least" when some files were unreadable. Alternative: leave the size out.

## Sorry-portainer: image removal, outside-prefix use, and the volume size walker (gentle-badger, 2026-11-08, round 2)

Decisions from Bart:

- Multi-tag images: Remove removes every tag. The confirmation says how many tags go.
- Images used only by another prefix: the button stays disabled, labelled "In use elsewhere". Use by this prefix's containers still shows "In use by …".
- Partial volume sizes: shown as "At least …" (already in `sizeText`).
- Scan interval: 30 minutes (`DefaultVolumeSizeInterval`).
- Allocations: a custom walker (`internal/podman/dirsize.go`).

Done:

- Change 01a1b701-1520-7fb1-ace8-ece493f1430f, "Images: remove every tag, and show use by containers outside the prefix". `RemoveImage` inspects the image and lists containers first. If any container (in or out of the prefix) runs it, the removal is refused before any tag is touched. Otherwise it runs `rmi --` on the ID for a single tag, or `rmi --` on each tag name for several. The last tag removal deletes the image. A failure part way reports how many tags went. `Image.used_outside_prefix` carries the outside use to the UI.
- Verified on real Podman with a throwaway two-tag image (imported from a tar, container created on it): in-use removal refused, tags unchanged; after the container was removed, both tags and the image were gone. Test objects deleted, none left.
- Browser check against the scratch mock: list shows "Used outside this prefix" and a disabled "In use elsewhere" button; the detail page shows "In use elsewhere". NOT checked in the browser: the multi-tag confirmation text on an enabled image.
- Walker (its own change, captured with this note): `directorySize` reads directories with getdents and sizes files with fstatat relative to the directory descriptor. It makes no path strings and no file-info objects; each directory level holds one descriptor and one reused buffer. Linux only. Behaviour changes: symbolic links are still not followed; a root that is itself a symlink is now an error (it was counted as zero bytes).
- Benchmark `BenchmarkVolumeSizeScan` (20 volumes, 50,000 one-byte files), Ryzen 9 9900X: now about 40 ms, 1.3 MB, 683 allocations per scan. Before: about 58 ms, 25.9 MB, 307,869 allocations.
- Verified on a real Podman volume (nested directories, a file symlink, a directory symlink): walk returned 3,001,000 bytes, complete. Temporary test file and volume deleted.
- Tests: `dirsize_test.go` (nested directories, multi-read directories, symlinked directories, missing root, cancellation, unreadable entries, existing cases moved from `volumesize_test.go`). `remove_test.go` covers single tag, several tags, the placeholder tag, refusal before any removal, and partial failure.

Open:

- Volumes have the same outside-prefix gap: a volume a container outside the prefix mounts is shown as not mounted here, and Podman's refusal is the only block. The volume side has not been changed.
- The image pre-check adds `ps --all` and `image inspect` calls to each removal. Cheap for this scale; noted.

## Sorry-portainer: volumes used from outside the prefix (gentle-badger, 2026-11-08, round 3)

- Done: a volume that a container outside the prefix mounts is flagged (`Volume.mounted_outside_prefix`). `volumeMounts` now reads every container's mounts in one call and keeps only the prefix's mounts in the list. The UI shows "Mounted outside this prefix" in the list, "In use elsewhere" in the detail chip, and a disabled "In use elsewhere" Remove button. The volume Remove path is unchanged on the backend: Podman refuses a mounted volume without partial effects.
- Verified on real Podman: a volume inside the prefix mounted only by a container outside it was flagged on the list and the detail; Podman's `volume rm` still refused it ("is being used"). Test objects deleted.
- Browser check on the scratch mock: list and detail show the new state. Not checked: the Remove confirmation on an enabled volume.
- Resolved: the outside-prefix gap for volumes listed under round 2.
