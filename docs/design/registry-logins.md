# Registry logins — Design

Status: partial — phases 1 and 2 landed (phase 1 tested live, phase 2 in the browser); open board gaps and the encoding option wait on Bart; not released
Owner: Bart
Last updated: 2026-11-12

## Implementation status

Tracking the gap between this design and the materialized implementation.
Tick items as they land and move them to Done.

### Done

- [x] **Decision — where logins are kept.** Bart (2026-11-12): "Master's
  secret store". Logins are sealed in the existing store on the master, not
  in a file on each host.
- [x] **Decision — how logins are managed.** Bart (2026-11-12): "Dashboard
  page". Needs a board from the design agent (see [Dashboard](#dashboard)).
- [x] **Decision — scope.** Bart (2026-11-12): "All hosts". One set of
  logins, used by every agent, including hosts enrolled later.
- [x] **Decision — delivery.** Bart (2026-11-12): "Push and keep", like stack
  secrets. The master pushes the whole set on agent connect, on unlock and on
  every change; the agent keeps it in memory and writes a 0600 `auth.json`
  on tmpfs only.
- [x] **Decision — showing a password.** Bart (2026-11-12): "Show after
  passphrase", as for stack secrets since 2026-11-07.
- [x] **Decision — test on save.** Bart (2026-11-12): "Test via an agent". An
  online agent signs in to the registry and the page shows the result; a
  failed test does not stop the save.
- [x] **Decision — what a login applies to.** Bart (2026-11-12): "Host or
  namespace". A login is for a registry (`docker.io`) or a path in it
  (`docker.io/myorg`); the longest match wins.
- [x] **Decision — username while locked.** Bart (2026-11-12): "Show when
  locked". The username is stored in the clear next to the sealed password.
- [x] **Decision — agent restart while locked.** Bart (2026-11-12): "Reuse
  tmpfs file". A restarted agent reads back the `auth.json` it wrote before;
  only a reboot clears it.
- [x] **Decision — stacks that need a private image after a reboot.** Bart
  (2026-11-12, after review): "Agent pre-pulls". Before `up`, the agent pulls
  each missing service image itself; a refused pull makes the stack wait for
  logins, and it starts in the background when they arrive.
- [x] **Decision — a corrupt `auth.json` at agent start.** Bart (2026-11-12,
  after review): "Start empty". Log, replace with an empty set, report "no
  logins"; the master pushes once unlocked.
- [x] **Review.** Codex and Antigravity, 2026-11-12. Findings and what was
  done with them are in [Review](#review).
- [x] **Design board.** The design agent's "Settings · Registry logins" board
  (`project/RegistryLogins.dc.html`) and the new rows on "Warnings and empty
  states" and "Stacks", 2026-11-12. What it adds is in
  [Follow-up: the design board](#follow-up-2026-11-12-the-design-board).
- [x] **Phase 1 — store.** `internal/secretstore/registries.go`: one
  sealed file per login (AAD binds key and username), add, replace (an
  empty password keeps the stored one), delete, list while locked, reveal
  with the passphrase, one decrypted set per push, the set ID.
- [x] **Phase 1 — protocol and relay.** `registries.sync`,
  `registries.test`, `HostInfo.RegistrySetID` in the hello; protocol
  version 5.
- [x] **Phase 1 — agent file and vault.** `internal/registryvault`: memory
  vault, per-host tmpfs file written with the checks below, read back at
  start (empty on corruption), `REGISTRY_AUTH_FILE` set before the first
  Podman command.
- [x] **Phase 1 — agent image check.** Keychain from the vault with
  longest-prefix matching, HTTPS-only transport, cache generation (`Flush`
  on every commit), the matched key in `ServiceImage`.
- [x] **Phase 1 — agent pre-pull and waiting for logins.** Missing images
  are pulled before `up`; a refused pull leaves the stack "waiting for
  registry login for <registry>"; `LoginsChanged` starts waiting stacks in
  coalesced background rounds when a set arrives. A refused pull during
  Update is reported as a failed pull, not as waiting.
- [x] **Phase 1 — server push.** `internal/server/logins.go`: per-host
  serialized (the host lock shared with the secret pusher), coalesced,
  background, 8-wide, backoff from 5 s doubling to 5 min; registries before
  secrets on connect and unlock.
- [x] **Phase 1 — web API.** `/api/registries` routes (below).
- [x] **Phase 1 — stack environment.** `REGISTRY_AUTH_FILE` and
  `DOCKER_CONFIG` refused as stack environment keys and pinned to the
  agent's own after the stack's environment.
- [x] **Phase 1 — last test per login.** Result, host and time kept with
  the login and recorded only if the login has not changed since; a login
  saved with no host online is tested by the first host that connects.
- [x] **Phase 1 — retry a host now.** `POST /api/registries/retry` `{host}`
  skips the backoff.
- [x] **Tests.** Listed under [Tests](#tests), with the differences noted
  there.
- [x] **Benchmark — push fan-out.** `internal/server/logins_bench_test.go`:
  1000 fake hosts, 64 logins at the limits. One decryption per round, the
  largest message 384,634 bytes, 20 rapid changes coalesced into at most 2
  rounds. `BenchmarkLoginRound`: about 67 ms, 506 MB allocated and 19,908
  allocations per round, nearly all of it encoding the same set once per
  host (see the encoding bullet below).
- [x] **Verify — podman-compose passes `REGISTRY_AUTH_FILE` to the `podman`
  it runs.** Settled by the live test: `podman-compose pull` of an image
  that is not local works with the agent's logins, and fails when the
  variable is withheld (checked by mutation). podman-compose 1.x exits 0
  when that pull fails, so the test checks for the image, not the exit
  code.
- [x] **Extended beyond the original plan — offline hosts' last seen.**
  `relay.OnDisconnect` reports a session that ends without a replacement,
  so the "On each host" card can say when an offline host was last seen.
- [x] **Extended beyond the original plan — why an image has no login.**
  `ServiceImage.login_key` and `login_state` (`none`, `not_on_host`,
  `refused`), and `Stack.waiting_login_key` / `waiting_login_state`, filled
  in by the master from its own set for the stack pages.
- [x] **Extended beyond the original plan — relay drain race.** A requested
  reconnect is reported even when the server's close arrives first
  (separate change; it made `TestDrainTimeoutEndsStuckSession` flaky).
- [x] **Phase 2 — Settings page.** `src/registries-ui.tsx` (page),
  `src/registries-store.ts` (Solid store, 5 s poll while visible),
  `src/registries-api.ts`, `src/registry-logins.ts` (the board's copy as
  pure functions): `/settings/registries` (`/settings` and any other
  `/settings/…` open it), Settings at the bottom of every sidebar, the
  section tab with its count, the logins list, the scope hint, the form,
  Show (passphrase, 30 s, Copy, Hide; locking hides it), Replace (an empty
  password keeps the stored one), Delete, the locked state and the "On each
  host" card with Retry now. Checked in the browser against a throwaway
  unlocked master.
- [x] **Phase 2 — stack pages.** The three login states (`none`,
  `not_on_host`, `refused`) on image rows and the checked line, and the
  "Waiting for a registry login" note with a Registry logins link on the
  stacks list and stack page. Unit-tested; the dev master's store is
  locked, so these states were not seen live.

### Outstanding

- [ ] **Board gaps — for Bart / the design agent.** Interim choices made
  where the board says nothing:
  - the waiting note for `waiting_login_state` `none` or `refused` (shows
    the agent's reason for now);
  - a host row while locked when the host holds an older set ("It gets the
    current logins when you unlock.");
  - the page when the store is not set up ("Create it on the Hosts page
    before adding registry logins.") or not configured;
  - "No hosts are enrolled yet." in the "On each host" card;
  - "Couldn’t copy" after a failed copy, and the error text in Retry now's
    tooltip;
  - an empty detail when a host's current set has no time;
  - login states on image rows use the muted tone, as the other check
    states do, where the board uses its problem colour;
  - the logins table needs 940 px (the board's column minimums), so at a
    1200 px window it scrolls sideways; fitting it means narrower minimums
    than the board's.
- [ ] **Option — encode a round's set once.** The relay encodes the same
  `registries.sync` message once per host. Accepting a pre-encoded payload
  would cut a 1000-host round's allocations from about 506 MB to one
  encoding (about 0.5 MB) plus per-host framing. For Bart to decide.
- [ ] **Defer — logins per host.** Bart chose one set for all hosts.
- [ ] **Defer — insecure (HTTP) registries.** Logins are sent over HTTPS only
  (below).
- [ ] **Defer — credential helpers and cloud registries' short-lived tokens**
  (ECR, GCR `gcloud` helpers). A stored username and password or access
  token only.

## Why this exists

The image check and Update that shipped in v0.2.10 (see
[image-updates.md](image-updates.md)) only work for public images. Both the
check (go-containerregistry's default keychain) and `podman pull` read the
agent account's own login files, and on jp1 and hel1 that account has none,
so a private Docker Hub repository shows "No access" and cannot be pulled.
Starting a stack whose image is private and not yet on the host fails the
same way.

A registry password is a secret, so it follows the rule Bart set for stack
secrets on 2026-09-24: a secret value never sits on an agent host's disk. It
may live sealed on the master and in memory on the agent. This doc is the
contract for keeping logins in the master's secret store and getting them to
Podman without writing them to disk.

## Goals

- Pull, check and update private images on every host from one set of
  logins.
- Manage logins in the dashboard: add, replace, delete, test, and show a
  password after re-entering the passphrase.
- No password on an agent host's disk; tmpfs only, gone at reboot. No
  password in logs, process arguments, API errors or relay errors.
- The check and Podman use the same logins, so a check that says "update
  available" means the pull will work.

## Non-goals (v1)

- Logins per host (decided: all hosts).
- Credential helpers, OAuth flows and short-lived cloud tokens.
- Registries reached over plain HTTP.
- A CLI for logins (could follow `admin-cli.md` later).
- Logins on the master itself: the master never talks to registries.

## Model

A login is:

| Field | Stored | Rules |
| --- | --- | --- |
| `key` | clear | `registry` or `registry/path`, normalized (below), at most 255 bytes |
| `username` | clear | 1–255 bytes of printable UTF-8, no `:` (Basic auth splits on the first `:`), no control characters |
| `password` | sealed | password or access token, 1–4096 bytes, no control characters |
| `updated_at` | clear | |

**Validating the key.** The server rejects (400, naming the rule) rather than
silently rewrites anything beyond case: no scheme, tag, digest, empty path
segment, or trailing slash. The registry part must parse as a registry name
(`name.NewRegistry` with strict validation on the part before the first
`/`); each path segment must be a valid repository path component.

**Normalizing.** Lower-case the registry part. `index.docker.io` and
`registry-1.docker.io` are stored as `docker.io`. Ports are kept
(`registry.example.com:5000`).

**Matching follows Podman exactly** (containers/image
`pkg/docker/config/config.go`, `authKeyLookupOrder` and
`normalizeRegistry`), so the check and the pull always pick the same login:
a namespace key matches only by literal prefix of the reference, while a
registry-wide `docker.io` login also applies to references written with
`index.docker.io` or `registry-1.docker.io`. A reference such as
`registry-1.docker.io/myorg/app` therefore uses a `docker.io` login but not a
`docker.io/myorg` one, in Podman and in the image check alike.

**Matching.** An image reference is normalized the same way (`alpine` is
`docker.io/library/alpine`; go-containerregistry's `index.docker.io/…`
becomes `docker.io/…`). For an image `R/P`, the login with the longest key
that equals `R`, or is a prefix of `R/P` ending at a `/` boundary, wins.
`docker.io/myorg` matches `docker.io/myorg/app`, not
`docker.io/myorganisation/app`. Podman's `containers-auth.json(5)` uses the
same rule for namespace keys, so both sides pick the same login.

**Limits.** At most 64 logins. Go's JSON encoder writes `<`, `>` and `&` as
six-byte escapes, so the worst-case set is about 64 × (4096 × 6 + 2 × 255 ×
6) ≈ 1.8 MiB, inside `protocol.MaxMessageSize` (4 MiB).

**The set ID.** The master keeps a random 128-bit set ID in the clear,
replaced on every put and delete. It is sent with each push and reported
back by the agent, so the master can tell whether a host has the current
set without decrypting anything, even while locked.

## Storage on the master

Under the store's directory, next to `values/<host>/<stack>/`:

```text
<state>/secrets/registries/<sha256-hex(key)>.json
<state>/secrets/registries/set-id
```

The file name is a fixed 64 characters whatever the key's length. Each file
holds `{key, username, updated_at, password_sealed}`, the password sealed
with the store's AES-256-GCM key and associated data
`"sorry-portainer/registry/v1\x00" + key + "\x00" + username`, so a file
edited to show another key or username no longer opens. Writes use the
store's existing atomic write and directory sync, under its `writeMu`.
Listing reads only the clear fields and works while locked. Put, reveal and
the push need the store unlocked. Reveal re-verifies the passphrase under
`unlockMu` and goes through the server's passphrase limiter, as stack-secret
reveal does.

## Delivery

**Protocol version 5** adds two operations and a hello field. Server and
agents are deployed together (no compatibility, Rule #8).

- `registries.sync` — master → agent: `{set_id, logins: [{key, username,
  password}]}`, the full set. The agent answers `{set_id}` once the set is
  committed (below), or an error.
- `registries.test` — master → agent: `{key, username, password}`. The
  agent runs `podman login --authfile <tmp> --password-stdin --username
  <username> <registry>`, with the password on stdin, never in arguments,
  and `<tmp>` a fresh 0600 file in its tmpfs directory, removed afterwards.
  That is exactly the sign-in Podman itself does, including for Basic-auth
  registries where go-containerregistry's transport only discovers the
  challenge and would report success for a wrong password. The answer is
  `{ok, failure, message}`; `failure` is `denied`, `unreachable` or `tls`;
  `message` is built by the agent from the failure kind and HTTP status
  (see [Error text](#error-text)), never Podman's raw output.
- **Hello.** `HostInfo` gains `registry_set_id` (empty when the agent holds
  no logins), so a host that rebooted while the store was locked shows as
  not having the current logins.

**The pusher on the master** runs in the background, not on the request's
context, so a closed browser tab doesn't cut a push short:

- One goroutine per host at most, under the same per-host lock the secret
  pusher uses, so a registry push and a secret push to one host never run
  at the same time. It reads the set from the store *after* taking the lock,
  so an older snapshot can never arrive after a newer one.
- Coalescing: a change marks every host "wanted"; a host already being
  pushed gets one more push afterwards with whatever is current then, not
  one per change.
- Fan-out is bounded (8 hosts at a time). The set is decrypted once per
  round and shared, then cleared when the round ends.
- A host whose push failed, or whose reported set ID differs from the
  current one, is retried with backoff (5 s doubling to 5 min) while it is
  connected and the store is unlocked.
- On connect and on unlock, registries are pushed *before* stack secrets, so
  a stack that a secret push starts already has its logins.
- Nothing is pushed while the store is locked; agents keep what they have.

**On the agent:**

- The set lives in memory (an `internal/registries` vault; values cleared on
  replace, best effort as for secrets), with its set ID.
- The file is `$XDG_RUNTIME_DIR/sorry-portainer/<host>-auth.json`, per host
  ID like the hook socket `<host>.sock`, so several agents under one user
  (the dev setup runs three) never share a file. Before writing, and again
  at start, the agent opens the `sorry-portainer` directory with
  `O_NOFOLLOW|O_DIRECTORY`, checks with `fstatfs` on that descriptor that it
  is tmpfs, and checks it is owned by the agent's uid with mode 0700. It
  writes a temp file with `openat` (`O_CREAT|O_EXCL|O_NOFOLLOW`, 0600)
  relative to that descriptor, fsyncs, and `renameat`s it into place. The
  agent refuses to start if the directory check fails, so a misconfigured
  host fails loudly instead of putting passwords on disk.
- The file is Podman's format,
  `{"auths": {"<key>": {"auth": base64(user:password)}}}`, plus a
  `"sorry-portainer": {"set_id": …}` member that Podman ignores.
- **Commit order.** A sync writes and renames the file first, and only then
  swaps the memory set, flushes the image-check cache and answers. If the
  write fails, memory and file both keep the previous set and the agent
  answers with an error, so the master shows the host as failed and retries.
  A deleted login therefore stays usable on that host only while its
  delivery is shown as failed.
- A sync whose set ID is not newer than one being applied is impossible by
  construction (the master serializes per host), but the agent still
  applies syncs one at a time under a mutex.
- **At start**, the agent reads its file back. If it is missing, unreadable
  or does not parse, it logs the problem, writes an empty set and starts
  with no logins and an empty set ID (Bart's decision). Then it sets
  `REGISTRY_AUTH_FILE` to the file's path in its own environment, before
  any Podman command; the file always exists from then on.
- `composeEnvironment` in `internal/stacks/stacks.go` applies the stack's
  environment over the agent's, so it pins `REGISTRY_AUTH_FILE` after the
  stack's values, as it already does for `PODMAN_COMPOSE_PROVIDER`.
  `checkEnvironment` refuses `REGISTRY_AUTH_FILE` and `DOCKER_CONFIG` as
  stack environment keys, so a stack can't point Podman at other
  credentials.

**The image check** uses a keychain built from the memory set (matching as
above) instead of `authn.DefaultKeychain`, so a stray `~/.docker/config.json`
can't disagree with Podman. An image with no matching login is checked
anonymously. Its HTTP transport refuses any request that isn't `https://`:
go-containerregistry otherwise tries plain HTTP alongside HTTPS for
private-network, loopback and `.local` registries (`name.Registry.Scheme`,
`transport.ping`), which would send a login unencrypted. That matches
Podman, which requires TLS unless `registries.conf` marks a registry
insecure. `ServiceImage` gains `login`, the matched key or empty, so the
dashboard doesn't need its own matching to tell "no login" from "login not
delivered".

**Cache generation.** The checker keeps a generation number, bumped by a new
`Flush` on every committed sync. A request records the generation it
started under and, when it finishes, stores its result only if that is still
current. A check asked for after a flush never joins a request started
before it.

**Pre-pull and waiting for logins.** Before every `up` (manual Start, Update
of a stopped stack, bringing a stack back after a reboot, and a start from a
secret push), the agent resolves each service's image (the image check's
resolution) and runs `podman pull` for each image not present locally,
before running podman-compose. podman-compose 1.0.6 exits 0 when a pull or
`podman run` fails, so this is the only point where the agent can tell why.

- A pull refused by the registry (401/403 in Podman's error) is a typed
  `missingLoginError`, like `missingSecretsError`: compose is not run, the
  stack's state says "waiting for registry login for `<registry>`", and the
  desired state stays up with no `UpBoot`.
- Any other pull failure is the stack's start error, as today.
- When a `registries.sync` commits, the agent starts, in the background and
  one at a time (as `Resume` does), each stack that is waiting for a login.
  The sync answers as soon as the set is committed; it never waits for
  stacks to start.

## Error text

Registries and proxies can echo request details in response bodies. Nothing
from a registry's body reaches a log, an API response or a relay error:

- Test, check and pull failures are reported as the failure kind, the HTTP
  status if there is one, and the registry name, in text the agent builds
  (`docker.io refused the login (401)`).
- Podman's output from `pull`, `login` and compose is passed through a
  scrubber before it is logged or returned: every current password and its
  base64 `user:password` form is replaced with `[login]`.
- Request logs never include `/api/registries` bodies.

## Web API

The key contains `/`, and Go's router decodes `%2F` before splitting the
path, so the key never goes in the path:

| Method and path | Works locked | Does |
| --- | --- | --- |
| `GET /api/registries` | yes | logins (key, username, `updated_at`, last test), the current set ID, and per host `{online, set_id, state, at, error, retry_at}` |
| `POST /api/registries` | no | `{key, username, password}`; 409 if the key exists; stores, starts the push, runs the test, returns `{login, test}` |
| `POST /api/registries/replace` | no | `{key, username, password?}`; 404 if missing; an empty password keeps the stored one; as above |
| `POST /api/registries/test` | no | `{key}`; tests the stored login again |
| `POST /api/registries/reveal` | no | `{key, passphrase}` → `{password}`, passphrase-limited |
| `POST /api/registries/delete` | no | `{key}`; deletes and starts the push |
| `POST /api/registries/retry` | no | `{host}`; pushes to that host now |

`test` is `{state: ok|failed|no_agent, failure, message, host}`. The server
tries online hosts in host-ID order and moves on from a host that is busy or
doesn't answer within 30 seconds; `no_agent` when none answered. A failed or
skipped test never undoes the save.

## Dashboard

The page layout and copy come from the design agent. What the board has to
cover:

1. Where the page lives (sidebar entry, or a section on the secret store).
2. The list: key, username, last set, which hosts have the current logins.
3. Add and edit: key (the namespace option, and that Docker Hub's official
   images are `docker.io/library/…`), username, password or token; the
   test result (OK, refused, registry not reachable, TLS failure, no agent
   online).
4. Show password after re-entering the passphrase; replace; delete with
   confirmation.
5. The store locked: list visible, add/edit/show disabled, the unlock form.
6. A host that doesn't have the current logins (offline, failed delivery and
   its error, or rebooted while the store is locked).
7. On a stack page: "No access" with a login that the host doesn't have yet,
   versus with no login at all (a link to add one); and a stack "waiting for
   registry login".

## Tests

- Store: put, list while locked, reveal, delete, tampering with key or
  username, the set ID changes on every write, limits, validation and
  normalization table.
- Matching: longest prefix, segment boundary, ports, and a table of Docker
  Hub spellings checked against containers/image's lookup for the same
  `auth.json` (registry-wide vs namespace keys).
- Agent file: tmpfs/symlink/mode refusals (a non-tmpfs directory and a
  symlinked one in a test filesystem), commit order on a failed write,
  read-back, corrupt file at start.
- Checker: generation drops a late result; HTTPS-only transport refuses
  `http://`.
- Pusher: per-host ordering under concurrent changes (an older set never
  arrives last), coalescing, retry after failure, registries before secrets,
  nothing while locked, set ID from the hello.
- Stack environment: `REGISTRY_AUTH_FILE` refused and pinned.
- Scrubber: password and base64 form removed from Podman output and errors.
- Live (`SORRY_PORTAINER_TEST_PODMAN=1`): a local `registry:2` with basic
  auth behind TLS with a test CA; `registries.test` OK and refused; a stack
  with a private image started, waiting for login, then started by the push;
  Update of a private image; a scan of the agent host's disk outside tmpfs
  and of the agent's and server's logs for the canary password and its
  base64 form.
- The benchmark in the checklist.

Built 2026-11-12, `internal/stacks/podman_registry_integration_test.go`. It
differs from the plan in three ways:

- The registry is go-containerregistry's in-process `pkg/registry` behind a
  Basic-auth handler and TLS, not a `registry:2` container. The test needs
  no image pull or published port.
- Rootless Podman reads `certs.d` from `$HOME/.config` only, and moving
  `HOME` would move the image storage, so instead of trusting the test CA
  the test points `CONTAINERS_REGISTRIES_CONF` at a private file that marks
  just that registry `insecure` (TLS without verification).
- The canary scan covers every output and log line the agent side
  produces, the stack directory, the test's config directory and the
  compose project directory, not the whole disk. The master's side is
  covered by the scrubber and HTTP tests.

## Performance

A push is one message per host on rare events (connect, unlock, change),
O(hosts) messages per change and nothing per pull. With coalescing and
8-wide fan-out, the master holds one decrypted set (at most about 260 KiB)
and at most 8 encoded messages (about 1.8 MiB each in the worst case) at a
time, whatever the number of hosts. Matching an image to a login is a scan
of at most 64 keys per check, behind the 15-minute cache. Pre-pull adds one
local `podman images` per `up` (already done for the image check) and a pull
only for images that are missing, which compose would have pulled anyway.

## Phasing

1. Store, protocol, agent, pusher, web API and stack-environment change,
   with the tests and the benchmark.
2. The dashboard page, once the design board exists.

## Questions only building can answer

- ~~Whether podman-compose passes `REGISTRY_AUTH_FILE` on.~~ It does;
  settled by the live test (checklist).

## Follow-up 2026-11-12: the design board

The board settles the page and adds a few behaviours the first draft did
not have:

- **Where it lives.** A Settings page for fleet-wide settings: Settings sits
  at the bottom of every sidebar (fleet and host pages), above the secret
  store status. The page has section tabs; Registry logins is the first and
  only one for now. Routes: `/settings/registries`; `/settings` opens the
  first section.
- **Add and Replace are different actions.** Add refuses a key that already
  exists ("Use Replace on its row instead"); Replace keeps the key fixed, and
  an empty password keeps the stored one. So the API has a create and a
  replace call rather than one `PUT`.
- **Last test per login.** The list has a "Last test" column (Works,
  Refused, Unreachable, Certificate problem, Not tested yet) and names the
  host it was tested on. The result, host and time are stored with the
  login, in the clear: they describe the login, not its secret.
- **Testing later.** A login saved while no host is online is tested by the
  first host that connects ("The first host that connects tests it and gets
  it").
- **Retry now.** A host whose delivery failed has a Retry now button, which
  skips the backoff.
- **Host rows.** "Has the current logins · Received X ago", "Sending…",
  "Delivery failed · Trying again in N s", "Offline · Last seen X ago",
  "Missing the logins" (the host reports no set while the store is locked).
  The card header counts "N of M hosts up to date".
- **Show** hides the password again after 30 seconds, and has Copy.
- **Stack pages.** "No access" splits in three, on rows and headers: "No
  login for this registry" (refused, no saved login matches), "Login not on
  this host yet" (a saved login matches but the host doesn't have the
  current set), "Login refused" (the host has the login and the registry
  refused it). The server works out which by matching the image against the
  stored keys (the same matcher as the agent) and comparing the host's set
  ID; the agent reports which login it used. The stacks list shows
  "Waiting for a registry login: <host> doesn't have the login for <key>
  yet. The stack starts as soon as it arrives.", linking to Registry logins.

## Review

Codex and Antigravity reviewed the first draft on 2026-11-12. Accepted and
applied:

- **tmpfs check on the wrong directory; symlinks** (both): check the
  `sorry-portainer` directory itself through an `O_NOFOLLOW` descriptor and
  write relative to it.
- **Credentials in error text** (both): the [Error text](#error-text)
  contract and a scrubber; the live scan also looks for the base64 form.
- **HTTP fallback** (Codex): confirmed in go-containerregistry v0.22.1;
  HTTPS-only transport.
- **Push ordering and stale snapshots** (both): per-host lock shared with
  the secret pusher, store read under the lock.
- **Memory/file not transactional** (Codex): file first, then memory; a
  failed write keeps the old set and is reported.
- **No retry while connected; request-bound push** (both): background
  pusher with retry and backoff.
- **Reboot retry can't detect a failed pull** (both): podman-compose 1.0.6
  exits 0; Bart chose pre-pull with "waiting for registry login".
- **Stack environment overrides `REGISTRY_AUTH_FILE`** (both): pinned and
  refused.
- **Registries pushed after secrets** (both): pushed before.
- **Delivery state wrong after a reboot or master restart** (both): the set
  ID, reported in the hello.
- **In-flight checks repopulate the cache** (both): cache generation.
- **The test didn't validate Basic-auth logins** (Codex) and **namespace keys
  don't parse as registries** (Antigravity): the test runs `podman login`
  against the key's registry part.
- **Encoded message could exceed 4 MiB** (Codex): password limit 4 KiB,
  key and username limits, worst case computed with JSON's escapes.
- **Hex file names too long** (both): SHA-256 of the key.
- **Shared file between agents under one user** (both): per host ID.
- **Fan-out not bounded or measured** (Codex): coalescing, 8-wide fan-out,
  one decryption per round, the benchmark.
- **`:` in usernames, control characters** (Antigravity): refused.
- **`index.docker.io` from go-containerregistry** (Antigravity): normalized
  when matching.

Rejected: Antigravity's claim that Podman treats `registry-1.docker.io` as a
separate host. containers/image's `normalizeRegistry` maps it, `docker.io`
and `index.docker.io` to one registry for registry-wide keys; namespace keys
match by literal prefix. The matcher mirrors that rule instead.
- **`%2F` in the path** (Antigravity): keys move into request bodies.
- **Reveal must share the passphrase limiter** (Antigravity): stated.
- **Missing `Flush` on the checker** (Antigravity): added with the
  generation.
- **Test host busy or half-open; inconsistent schema** (Antigravity): try
  hosts in order with a timeout; one `test` shape.
- **Missing file with `REGISTRY_AUTH_FILE` set** (Antigravity): the agent
  always writes the file before setting the variable; the verify bullet is
  gone.
- **Corrupt file at start** (Antigravity): Bart chose "Start empty".
- **Frontend would duplicate matching** (Antigravity): `ServiceImage.login`.
- **Key validation vague** (Antigravity): reject rather than rewrite, with
  rules.

Antigravity's note that the test registry must not be on
`localhost` (the image check treats `localhost/…` as local) is taken into the
live test, which uses `127.0.0.1:<port>`.

## References

- [stack-secrets.md](stack-secrets.md): the store, push-and-keep, reveal,
  `missingSecretsError` and bringing stacks back after a reboot.
- [image-updates.md](image-updates.md): the check and Update that need this.
- Podman `containers-auth.json(5)`: file format, namespace keys,
  `REGISTRY_AUTH_FILE`; `podman-login(1)`: `--password-stdin`, `--authfile`.
