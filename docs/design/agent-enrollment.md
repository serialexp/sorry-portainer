# Agent Enrollment and Certificate Rotation — Design

Status: partial — Phase 1 and the Phase 2 UI implemented (2026-11-05); the rsansible role is outstanding
Owner: Bart
Last updated: 2026-11-05

## Implementation status

Tracking the gap between this design and the materialized implementation. Tick
items as they land and move them to Done.

### Done

- [x] **Decision — enrollment shape.** Bart (2026-11-05): one-time enrollment tokens over the network (option B), rather than certificate requests relayed through rsansible (option A). See [Options considered](#options-considered).
- [x] **Decision — certificate lifetime.** Bart (2026-11-05): build rotation now. Agent and server certificates are valid for 90 days and renewed automatically; see [Rotation](#rotation).
- [x] **Decision — tokens across a server restart.** Bart (2026-11-05): memory only. A restart invalidates every outstanding token.
- [x] **Decision — production agent layout.** Bart (2026-11-05): the agent's config, credentials and state all live in `/var/lib/sorry-portainer-agent` (owned by the agent user, 0700), because rotation means the agent must be able to replace its own credential files. `setup` and its unit change from `/etc/sorry-portainer/agent.json` to that directory. This settles the state-directory half of the matching item in `stack-secrets.md`; the OCI hooks directory stays in the agent user's home, next to its `containers.conf.d` drop-in.
- [x] **Review.** Codex and Antigravity reviewed the first draft on 2026-11-05; the findings and what changed are in [Review](#review-2026-11-05).

- [x] **Phase 1 — credential files.** One PEM bundle per credential (`agent.pem`, `control-server.pem`), replaced with a single rename; a staged key (`agent-next.key`) for an enrollment or renewal in progress. `internal/pki/bundle.go`, `internal/atomicfile`.
- [x] **Phase 1 — host registry.** `<state_dir>/hosts.json`, copy-on-write behind an atomic pointer, one durable rewrite per change, fails closed. `internal/enrollment/registry.go`.
- [x] **Phase 1 — state directory lock.** `server.lock`, taken by the server, `init` and `pki reset`.
- [x] **Phase 1 — allowlisted, atomic admission.** Early check before the upgrade, promotion of a pending certificate, final `Authorize` under the relay's session lock. `internal/server/control.go`, `internal/relay/remote.go`.
- [x] **Phase 1 — enrollment tokens.** `internal/enrollment/token.go`, `service.go`.
- [x] **Phase 1 — enrollment endpoint.** `POST /control/enroll`; an oversized body gets 413.
- [x] **Phase 1 — agent `enroll` command.** `sorry-portainer-agent enroll`; `internal/agentcred/enroll.go`.
- [x] **Phase 1 — agent certificate rotation.** `internal/agentcred/renew.go`; the agent's main loop runs the renewer with `OnRenewed = RequestReconnect`.
- [x] **Phase 1 — server certificate rotation.** `internal/controlcert`.
- [x] **Phase 1 — local admin socket and CLI.** `internal/admin`; `hosts token|list [--json]|remove`. Extended beyond the plan: the CLI also trusts a server running as the state directory's owner (see the note under [Admin socket and CLI](#admin-socket-and-cli)). The socket code shared with the agent's secret socket moved to `internal/unixsock`.
- [x] **Phase 1 — control listener hardening.** `internal/server/listeners.go`, `control.go`.
- [x] **Phase 1 — `server pki reset`; remove `server agent --create`.** `internal/provision`; `just dev-init` enrolls the development agents with tokens and migrated the existing development state (2026-11-05).
- [x] **Phase 1 — server config.** `control.ca_key_file`, `control.credential_file` and `control.server_names` in `server.json` and its schema; `server init --server-name` (repeatable).
- [x] **Phase 1 — production agent layout.** `setup` creates `/var/lib/sorry-portainer-agent` for the agent user (0700) and its unit runs `--config /var/lib/sorry-portainer-agent/agent.json`; setup prints the enroll step.
- [x] **Phase 1 — tests and benchmarks.** Every item under [Tests](#tests); benchmarks and their results under [Performance](#performance). Mutation-checked: admitting a pending certificate, accepting an expired one, reusing a token with another key, pinning to any CA in the chain, removal without disconnect, and the admin socket's peer check each fail a test.
- [x] **Extended beyond the plan — graceful server shutdown.** SIGTERM stops both listeners and gives in-flight requests 10 seconds; agent sessions close with the process and agents reconnect.
- [x] **Phase 2 — web API.** The `/api/enrollment/` routes in [Web UI](#web-ui), behind the dashboard session; `internal/server/enrollment.go`. Extended beyond the plan: the token answer says whether the host is already enrolled (`reenroll`), and its command then ends in `--replace`.
- [x] **Phase 2 — UI.** The `/enrollment` page (`src/enrollment-ui.tsx`, `enrollment-store.ts`, `enrollment-api.ts`): enroll a host with the token and agent command shown once, outstanding tokens with revoke, enrolled hosts including offline ones with certificate expiry and renewal state, remove with confirmation. Its own page rather than part of the host page; see the note under [Web UI](#web-ui).
- [ ] **Phase 2 — rsansible role.** Install the master and agents from a release and enroll each agent with a fresh token; covered in its own doc.
- [ ] **Defer — control CA rotation.** The CA is valid for 10 years; `server pki reset` replaces it, and every agent must then be enrolled again.
- [ ] **Defer — master-enforced host prefix.** The prefix stays in the agent's config; the master records the one the agent reports at enrollment but does not enforce it.

## Why this exists

Today `sorry-portainer-server agent --create HOST_ID` generates an agent's
private key on the master and writes a bundle that someone has to carry to the
agent host. The control listener then accepts any certificate the control CA
has signed, for ten years. Two things are wrong with that for a real
deployment across hel1 and jp1:

- the agent's private key is created on the wrong machine and travels through
  whatever copies it (a laptop, rsansible's temp files), and
- a leaked or superseded certificate can never be refused, because the master
  keeps no record of which hosts it enrolled.

This doc is the contract for replacing both: a key that never leaves its host,
an allowlist on the master that decides which certificate is current for each
host, and short-lived certificates that renew themselves.

## Goals

- An agent's private key is generated on its own host and never leaves it.
- Enrolling a host needs one short-lived secret that can be passed around
  (pasted from the UI, or handed from the master to the agent by rsansible),
  and that is worthless once used or expired.
- The agent authenticates the master during enrollment without trusting any
  public CA or the network: the token carries the control CA's fingerprint.
- The master only accepts enrolled hosts presenting their current certificate.
  Removing a host or enrolling it again takes effect immediately, including
  for a session that is already connected or still being set up.
- Agent and server certificates are short-lived and renew themselves; an
  agent that stays in touch with the master never needs an operator for its
  certificate.
- No crash, lost response or restart at any point leaves a host that can no
  longer connect while its old certificate is still valid.
- Scriptable without the admin password: rsansible runs a command on the
  master as the service user (or root).

## Non-goals (v1)

- Rotating the control CA automatically.
- CRLs or OCSP. The allowlist is the revocation mechanism; nothing outside the
  master needs to check agent certificates.
- Approving enrollments interactively (an agent appearing as "pending" until an
  admin accepts it). The token is the approval.
- Multiple administrators or per-token permissions.
- More than 1,000 hosts per master (the size the registry is benchmarked at).

## Options considered

- **(A) Certificate request relayed by rsansible.** The agent prints a CSR,
  rsansible carries it to the master, which signs it from the CLI. Only public
  material moves and nothing new listens on the network, but enrollment always
  needs SSH to both machines.
- **(B) One-time token over the network (chosen).** The admin creates a token
  for a host ID; the agent sends its CSR with the token to an endpoint on the
  control port. Works from a pasted command, like Portainer's agent setup, at
  the cost of the control port accepting connections without a client
  certificate.

## Certificates

| Certificate | Issued by | Lifetime | Usages | Names |
| --- | --- | --- | --- | --- |
| Control CA | itself | 10 years | cert sign | `CN=sorry-portainer control CA` |
| Server | CA, by the server itself | 90 days | server auth only | `control.server_names`: DNS names in `DNSNames`, IPs in `IPAddresses` |
| Agent | CA, at enrollment or renewal | 90 days | client auth only | `CN=<host-id>`, URI `sorry-host://<host-id>`, no DNS names |

- All keys are ECDSA P-256. A CSR with any other key is refused.
- `NotBefore` is backdated by 15 minutes for clock skew. Hosts are expected to
  run time synchronization; a TLS failure that names an expired or not yet
  valid certificate is reported with both clocks' view (the agent logs its own
  time and the certificate's validity window).
- No leaf outlives the CA: `NotAfter` is capped at the CA's. The server logs a
  warning at start once the CA has less than a year left.
- Only the public key is taken from a CSR; subject, names and extensions are
  the master's.
- The `host-<id>` DNS-name fallback in `certificateIdentity` is removed: the
  URI SAN is the only identity, and a certificate with server auth is never
  accepted as an agent.

### Credential files

Each credential is one PEM file holding the private key and then the
certificate, mode 0600, owned by the process that uses it:

- agent: `<agent dir>/agent.pem`; the CA is `<agent dir>/control-ca.crt`;
- server: `<state_dir>/control-server.pem`; CA key and certificate stay as
  `control-ca.key` and `control-ca.crt`.

A new credential is written to a temporary file in the same directory,
fsynced, renamed over the old one, and the directory is fsynced. One rename
replaces key and certificate together, so no crash leaves a mismatched pair.
The process loads the file, checks that the key matches the certificate and
that the certificate is the one it expects, and only then publishes it in
memory (an `atomic.Pointer[tls.Certificate]`).

While an enrollment or renewal is in progress, the new key is staged in
`<agent dir>/agent-next.key` (0600, fsynced) before the CSR is sent, so a
retry after a crash or a lost response uses the same key.

## Token

A token is created for exactly one host ID. Its text form is

```
spt1.<token-id>.<secret>.<ca-sha256>
```

All three fields are unpadded base64url (`base64.RawURLEncoding`):

- `token-id`: 16 random bytes, so the master can look the token up without
  comparing secrets. It is the only part of a token ever logged.
- `secret`: 32 random bytes. The master keeps only its SHA-256 and compares in
  constant time.
- `ca-sha256`: SHA-256 of the control CA certificate (DER). The agent uses it
  to authenticate the master before sending anything.

The host ID is not part of the text: the agent states it, and the master
refuses a token used for another host. Tokens live only in the server's
memory. Lifetime: default 1 hour, at most 24 hours. Creating a token for a
host that already has one replaces the old one. At most 64 can be outstanding:
creating one purges expired tokens first, then refuses with an error if 64
unexpired tokens remain. Removing a host revokes its token.

A token is spent by the enrollment that commits with it. It then stays in
memory, marked used and holding the issued certificate, until its expiry, so
that the same agent retrying with the same key gets the same certificate back
(see below). Anyone else using it is refused.

## Enrollment flow

```
admin / rsansible                master (jp1)                    agent host (hel1)
-----------------                ------------                    -----------------
hosts token --host hel1  ─────►  admin.sock: new token
                         ◄─────  spt1.…   (printed once)
token → agent host (stdin) ────────────────────────────────────►  sorry-portainer-agent enroll
                                                                  stage P-256 key in agent-next.key
                                 TLS, no client certificate  ◄──  verify the master (pinning, below)
                                 POST /control/enroll        ◄──  {token, host_id, prefix, csr}
                                 checks, sign, hosts.json
                                 (durable), close any live
                                 hel1 session
                                 {certificate, ca}           ──►  check the reply, write agent.pem,
                                                                  control-ca.crt, agent.json;
                                                                  remove agent-next.key
```

### Request and response

```json
POST /control/enroll
{"token": "spt1.…", "host_id": "hel1", "prefix": "hel1-", "csr": "-----BEGIN CERTIFICATE REQUEST-----…"}

200 {"certificate": "-----BEGIN CERTIFICATE-----…", "ca": "-----BEGIN CERTIFICATE-----…"}
4xx/5xx {"error": "…", "code": "…"}
```

Error codes: `invalid_request` (400: malformed JSON, token or CSR, host ID
fails `protocol.ValidHostID`), `invalid_token` (401: unknown, expired, wrong
secret, or used by another key; one code for all so a caller learns nothing
about which), `wrong_host` (403: valid token for another host), `unsupported_key`
(400), `busy` (503: the unauthenticated concurrency limit is reached),
`unavailable` (503: the registry cannot be written).

### Checks, in order

1. Decode the token strictly (prefix, three fields, exact lengths). Anything
   else is `invalid_request`, and the raw text is not logged.
2. Look up the token ID; check expiry and the secret (constant time).
3. Check the host ID against the token's. A mismatch does **not** spend the
   token.
4. Parse the CSR, verify its signature, check its key. Failures do not spend
   the token: the secret was right, so the caller is the admin's agent, and
   letting it retry costs nothing.
5. If the token is already used: same public key → return the stored
   certificate; other key → `invalid_token`.
6. Commit, under the registry's write lock: the token is still unused and the
   host has not been removed since step 2; sign; write `hosts.json` durably
   with the new certificate as the host's current one (replacing any earlier
   enrollment, and clearing its pending one); publish the new registry; mark
   the token used. Then close any live session for the host.

### Pinning the master

The enrollment and renewal clients use a fresh `http.Transport` each time,
with redirects refused (`CheckRedirect` returns an error) and no proxy for the
control URL unless `HTTPS_PROXY` says otherwise. Enrollment sends no client
certificate at all, so an expired or foreign certificate on disk cannot break
the handshake. The TLS config sets `InsecureSkipVerify` and does the check in
`VerifyConnection`:

1. Find a certificate in the presented chain whose SHA-256 equals the pinned
   fingerprint and that is a CA. None → fail.
2. Verify the leaf with that certificate as the **only** root, for the server
   name, with server-auth usage, at the current time. An unrelated chain with
   the pinned CA appended does not verify.

The server always sends the CA in its chain (the in-memory `tls.Certificate`
holds leaf and CA). The agent then checks the reply before writing anything:
the returned CA's fingerprint is the pin, and the certificate's public key,
host ID and usages are what it asked for. After enrollment the agent verifies
the master with the normal `RootCAs` = the pinned CA.

The server name defaults to `URL.Hostname()` of the server URL (never
`host:port`), and is written to `agent.json`.

## Agent command

```
sorry-portainer-agent enroll \
  --server-url wss://HOST:PORT/control/agent \
  --host-id HOST_ID \
  --dir /var/lib/sorry-portainer-agent \
  --token-file -            # or a path; the token is never a command-line argument
  [--prefix PREFIX] [--server-name NAME] [--compose-provider PATH] [--replace]
```

- The enrollment URL is derived from the server URL: same host and port,
  `https://…/control/enroll`.
- `--dir` holds `agent.json`, `agent.pem`, `control-ca.crt` and the agent's
  state. It must be owned by the user running the command and not be group or
  world accessible.
- An existing `agent.json` is refused unless `--replace` is given. `--replace`
  keeps the other settings in `agent.json` (state directory, socket, hooks
  directory, compose provider, prefix unless `--prefix` is given) and the
  stack state, and replaces only the credentials and the server settings.
- A running agent keeps the credential it loaded; after `--replace`, restart
  the service. The master closes the old session as soon as the new
  enrollment commits, so the old agent stops working either way.
- Files are committed only after the reply passed its checks, so a failed
  enrollment leaves no half-configured agent; `agent-next.key` stays for a
  retry with the same token and key until the enrollment succeeds.

## Rotation

### Agent certificates

The agent checks its certificate at start and every 12 hours. Its renewal
threshold is 30 days before expiry minus a random 0–7 days, chosen per
certificate, so a fleet enrolled together does not renew together. Renewals
are serialized in one goroutine.

1. Stage a new P-256 key in `agent-next.key` (or reuse the staged one).
2. `POST /control/renew` with the **current** certificate as the TLS client
   certificate and `{"csr": "…"}` as the body, over a fresh transport that is
   closed afterwards. The master checks the client certificate is the host's
   current one, then under the write lock: the host and its current
   certificate are unchanged; if the host already has a pending certificate
   for the same public key, return it; otherwise sign, write `hosts.json`
   durably with the new certificate as pending (replacing any other pending
   one), publish, and return `{"certificate": "…"}`.
3. The agent checks the certificate (its key, host ID, usages, issuer), writes
   `agent.pem`, publishes it in memory, and removes `agent-next.key`.
4. The agent drains its relay session: it stops accepting new requests from
   the master (answering `busy`), waits for in-flight ones up to 5 minutes,
   closes the session and dials again at once. That dial presents the new
   certificate, which the master accepts as pending, promotes to current
   (durable write first) and drops the old one.

A pending certificate is accepted on `/control/agent` only; `/control/renew`
requires the current one. Crash windows:

| Crash or failure | State afterwards | Recovery |
| --- | --- | --- |
| before the master answered | old pair; maybe a staged key; maybe a pending entry | renews again with the staged key; the master returns the same pending certificate, or signs one if none |
| after the master answered, before `agent.pem` is written | old pair, staged key, pending entry | same as above |
| after `agent.pem` is written, before the reconnect | new pair, pending on the master | the next dial (start or reconnect) promotes it |
| master restarts at any point | `hosts.json` holds current and pending | both still accepted |
| renewal keeps failing | old pair, still current | retried with backoff, 1 minute to 1 hour, logged |

An agent whose certificate has expired cannot connect or renew and must be
enrolled again. The host page shows each host's certificate expiry and warns
when it is less than 7 days away.

### Server certificate

The server holds the CA key, so it re-issues its own certificate (names from
`control.server_names`, new key) at start when the file is missing, does not
cover the configured names, lacks server-auth only usage, or has less than 30
days left, and again whenever a daily check finds less than 30 days left. It
writes `control-server.pem` as above and serves it through `GetCertificate`,
the TLS config's only certificate source, so clients with and without SNI
(IP addresses) get the current one. Agents verify it against the CA, so
nothing changes on their side.

## Host registry

`<state_dir>/hosts.json` (0600):

```json
{
  "version": 1,
  "hosts": {
    "hel1": {
      "enrolled_at": "2026-11-05T12:00:00Z",
      "prefix": "hel1-",
      "current":  {"sha256": "…", "not_after": "2027-02-03T12:00:00Z"},
      "pending":  {"sha256": "…", "not_after": "2027-05-04T12:00:00Z", "certificate": "-----BEGIN CERTIFICATE-----…"}
    }
  }
}
```

The pending entry keeps its whole certificate so a repeated renewal with the
same key can be answered without signing again.

**Ownership.** The running server is the only reader and writer, guaranteed by
the state-directory lock. The CLI never opens the file.

**Copy-on-write.** The published registry is an immutable value behind an
`atomic.Pointer`. Readers (admission, renewal checks, the host list) never
take a lock and never wait for disk. Mutations take a single write mutex,
build the next value, persist it, and only then publish it.

**Persistence.** Write a temporary file, fsync, rename over `hosts.json`, fsync
the directory. If any step before the rename fails, nothing changed and the
request fails (`unavailable`). If the rename succeeded but the directory fsync
failed, the new content is what the file holds, so it is published, but the
registry is marked degraded: further mutations are refused and logged until
the server restarts. A credential is never handed out, and a removal never
acknowledged, before its write has been made durable.

**Startup.** A missing `hosts.json` means no hosts are enrolled (a fresh
`server init` writes an empty one). A file that cannot be parsed, has an
unknown version, or is group/world accessible stops the server: it never falls
back to accepting any certificate the CA signed. Restoring an old `hosts.json`
from a backup brings back the certificates current at the time, including
ones removed since; restore it only together with a review of the host list.

## Admission

`ControlHandler` passes `relay.Remote.Serve` the host ID and the SHA-256 of
the presented certificate, plus an `Admit` callback. `Serve` reads the agent's
hello, then, while holding the remote's session lock, calls `Admit`, which
checks the current registry: the host is enrolled and the certificate is its
current or pending one, and still within its validity. A pending certificate
is promoted by `Admit` (durable write, then publish) before the session is
inserted. Only after the session is inserted does `OnConnect` run.

Removal and re-enrollment publish the new registry first and close the
host's session (under the remote's session lock) second. A connection that
passed the early check before the removal either was inserted before the
close, and is closed by it, or calls `Admit` after the publication, and is
refused. Renewal commits check under the write lock that the host's current
certificate is still the one the request authenticated with.

The early check in `ControlHandler` (before the WebSocket upgrade) is the same
lookup, so unknown hosts and old certificates are refused with `403` without
an upgrade.

## Admin socket and CLI

The server listens on `<state_dir>/admin.sock` once it holds the state lock,
so it may remove a stale socket file (never another kind of file). The socket
is 0600; peers must run as the server's own uid or as root (`SO_PEERCRED`).
The CLI checks the server end the same way. One JSON request and one JSON
response per connection, 64 KiB at most for a request and 16 MiB for a
response (a listed host takes about 160 bytes), 5-second deadline, at most
4 connections at a time.

```json
{"action": "token",  "host_id": "hel1", "ttl": "1h"}  → {"token": "spt1.…", "expires_at": "…"}
{"action": "list"}                                    → {"hosts": [{"host_id": "…", "prefix": "…", "enrolled_at": "…", "current_not_after": "…", "pending": false, "online": true}], "tokens": [{"host_id": "…", "expires_at": "…"}]}
{"action": "remove", "host_id": "hel1"}               → {}
any failure                                           → {"error": "…", "code": "…"}
```

```
sorry-portainer-server hosts token  --state-dir DIR --host HOST_ID [--ttl 1h]
sorry-portainer-server hosts list   --state-dir DIR
sorry-portainer-server hosts remove --state-dir DIR --host HOST_ID
```

Implementation note (2026-11-05): "the CLI checks the server end the same
way" would refuse the common case of root running the CLI against a server
running as its service account. The CLI therefore trusts a server running as
itself, as root, or as the owner of the state directory, which is the
server's account; any other uid holding the socket path is refused. The
server side is as designed: its own uid or root.

`hosts token` prints only the token on stdout, so a script can capture it.
`hosts list --json` prints the listing as JSON for scripts.
Running as the service user (or root) on the master is the authority to
enroll hosts; that account must not be shared with untrusted workloads.

rsansible runs `hosts token` on the master with `no_log`, and feeds the result
to `enroll --token-file -` on the agent host through the command's stdin, so
the token never appears in a process list.

## Control listener

The control listener changes from `RequireAndVerifyClientCert` to
`VerifyClientCertIfGiven`: a certificate that is presented is still verified
against the control CA during the handshake. Session tickets are disabled, so
every connection runs a full handshake and verification. Every protected
request checks `r.TLS.PeerCertificates`, the certificate's validity and the
registry itself; nothing relies on the handshake alone.

| Path | Client certificate | Notes |
| --- | --- | --- |
| `/control/agent` | required: enrolled, current or pending | pending is promoted at admission |
| `/control/renew` | required: enrolled, current | `POST`, 64 KiB body |
| `/control/enroll` | none needed | `POST`, 64 KiB body, unauthenticated limit |
| anything else | — | 404 |

Limits:

- `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s on enroll and renew (the
  WebSocket route clears its deadlines after the upgrade, as now),
  `WriteTimeout` 30 s for the same, `IdleTimeout` 2 min, `MaxHeaderBytes`
  16 KiB, on both the control and web listeners.
- Requests without a client certificate share a limit of 8 at a time; the
  ninth gets `busy` at once. Requests with a verified certificate are not
  counted against it, so an enrollment flood does not starve agents beyond
  the TLS handshakes it costs.
- Failed enrollments are logged at most once per remote address per minute,
  with a count of suppressed lines, from a bounded table (1,024 entries,
  oldest dropped).

## Web UI

Phase 2. The host page gets an "Enroll host" form (host ID, lifetime) that
shows the token and the agent command once, a list of outstanding tokens with
expiry and revoke, and the enrolled hosts from the registry, so a host that is
enrolled but offline is still visible, with its certificate expiry. Remove
asks for confirmation.

Web API (session required):

| Method and path | Purpose |
| --- | --- |
| `GET /api/enrollment/hosts` | registry entries plus online state and expiry |
| `DELETE /api/enrollment/hosts/{host}` | remove a host and its token, disconnect it |
| `GET /api/enrollment/tokens` | outstanding tokens: host, expiry (never the secret) |
| `POST /api/enrollment/tokens` | `{host_id, ttl}` → `{token, expires_at, command}` |
| `DELETE /api/enrollment/tokens/{host}` | revoke |

Follow-up (2026-11-05): the form and lists became a page of their own,
`/enrollment`, linked from the sidebar and the hosts overview, instead of
part of the host page: a host being enrolled has no host page yet, and an
enrolled host that is offline is not in the connected-host list the host
pages come from. The issued token lives only in the page's store and is
dropped when the page closes, when Done is pressed, or when its token is
revoked. A host's certificate is shown as overdue once less than 30 days
are left without a pending renewal, since agents renew 30 to 37 days before
expiry.

## `server pki reset` and development setup

`sorry-portainer-server pki reset --state-dir DIR` (server stopped; it takes
the state lock) moves `control-ca.*`, `control-server.*` and `hosts.json`
into `<state_dir>/pki-retired-<timestamp>/`, creates a new CA and server
certificate for `control.server_names`, writes an empty registry, and updates
the `control` section of `server.json` in place (other settings, including the
admin password, are kept). Every agent must then be enrolled again.

`server agent --create` is removed. `just dev-init` becomes idempotent:

1. `server init` if `server.json` does not exist; `server pki reset` once if
   it exists but has no `control.server_names` (the development state from
   before enrollment).
2. Start the server unit; wait until `hosts list` answers.
3. For each of `local-a`, `local-b`, `local-c` without an `agent.pem`: create
   a token, run `enroll --replace` into `.dev/state/agents/local-X/` (stack
   state and settings kept), restart that agent's unit.

## Performance

Budget for 1,000 enrolled hosts:

- Admission and renewal checks are a lock-free read of the published registry:
  no allocation beyond the lookup, never blocked by a registry write.
- A registry write (one per enrollment, removal, renewal or promotion; about
  one per host per 60–90 days in steady state) rewrites a file of roughly
  0.5 MB at 1,000 hosts. Budget: under 50 ms including fsync on an SSD.
- Renewal jitter spreads a fleet enrolled at once over 7 days.
- Tokens are bounded at 64, the failure-log table at 1,024 entries,
  unauthenticated requests at 8 at a time.

Measured 2026-11-05 on Bart's workstation (Ryzen 9 9900X, NVMe):

| Benchmark | Result |
| --- | --- |
| `enrollment.BenchmarkRegistryWrite`, 1,000 hosts, every tenth with a pending certificate (320 KB file) | 6.5 ms per durable write |
| `enrollment.BenchmarkCheckDuringWrites`, 1,000 hosts, registry rewritten continuously | 16 ns per check, 0 allocations |
| `integration.BenchmarkAgentAdmission/idle` (handshake, checks, upgrade) | 0.7 ms |
| `integration.BenchmarkAgentAdmission/flood`, 64 clients opening new TLS connections to `/control/enroll` (about 6,900 requests/s, same machine) | 9.9 ms |

Under the flood the agent competes for CPU with the flood's handshakes, which
run on the same machine as the server here; it never waits for an
enrollment slot. The admission check first allocated two strings per
connection to compare fingerprints; `pki.Fingerprint.Matches` removed them.

Benchmarks (`go test -bench`, not gated): registry persist at 1,000 hosts;
admission lookups while writes run concurrently (lookups must not wait for
the write); agent admission latency under a flood of unauthenticated enroll
requests at the concurrency limit.

## Tests

Time is injected everywhere (`now func() time.Time`), never slept.

- Credential files: one rename replaces key and certificate; a mismatched or
  unexpected file is not published; crash points simulated by failing each
  write step.
- Token: format round trip and strict decoding; expiry; single use; wrong
  host does not spend; bad CSR does not spend; used token with the same key
  returns the same certificate, with another key is refused; replacement;
  the 64 bound with purging; removal revokes; the secret is never stored or
  logged.
- Endpoint: happy path; every error code; certificate contents (only the
  public key from the CSR, client auth only, backdating, CA cap); body and
  header limits; the unauthenticated concurrency limit.
- Pinning: refuses a chain without the pinned CA, a leaf for another name, a
  leaf not signed by the pinned CA with that CA appended, a redirect; accepts
  the real server by DNS name and by IP.
- Admission: unknown host, superseded certificate, expired certificate,
  pending promotion (durable before admission); removal racing a connection
  between the early check and `Admit` (deterministic interleaving through a
  test hook), re-enrollment closing a live session.
- Rotation: threshold and jitter; idempotent renewal with the same staged key;
  each crash window in the table; renewal authenticated with a pending
  certificate is refused; a renewal racing a removal does not resurrect the
  host; drain and reconnect promotes; the server re-issues its own certificate
  and serves it without restart, with and without SNI.
- Registry: atomic rewrite; degraded state after a directory fsync failure;
  startup refuses a corrupt, unknown-version or world-readable file; the
  state lock refuses a second server.
- Admin socket: uid checks both ways, limits, every action.
- End to end in `internal/integration`: enroll three agents through a real
  control listener, connect, renew one (forced threshold) and see it promoted,
  remove one and see it refused.

## Review (2026-11-05)

The first draft was reviewed by Codex and Antigravity. What changed:

- **Rotation state was not durable** (both). The draft rewrote `hosts.json`
  only on enrollment and removal; a restart after a renewal would have locked
  out an agent that had already installed its new certificate. Renewal and
  promotion now write durably before they take effect.
- **Promotion could wait indefinitely** (both). A healthy session never
  redials, so the draft's "next dial" might be weeks away. The agent now
  drains and reconnects right after installing the new certificate.
  `GetClientCertificate` does not change an established connection's identity,
  as Codex pointed out; the draft's test item was wrong.
- **Key and certificate as two renames** (Codex). Replaced by one bundle file.
- **Concurrent or repeated renewals** (Codex). Renewals are serialized on the
  agent, the key is staged before the request, and the master answers a
  repeated request for the same key with the same certificate.
- **Admission raced removal** (Codex). Admission and session registration are
  now one step under the remote's lock, after the hello.
- **Removal did not revoke tokens** (Codex). It does now, and enrollment
  commits check the host was not removed meanwhile.
- **Unauthenticated load** (both). Added the concurrency limit, header limits,
  timeouts, and rate-limited failure logging, and a benchmark for agent
  admission under an enrollment flood. Per-address rate limiting was not
  added: the concurrency limit bounds the work, and the 256-bit secret makes
  guessing pointless.
- **Token spent on a typo** (Antigravity). Wrong host and bad CSRs no longer
  spend it; a lost response can be retried with the same key.
- **Registry failure semantics** (Codex). Publication point, degraded state,
  fail-closed startup, and the backup caveat are specified.
- **State lock and admin socket** (Codex, Antigravity). Added the state lock;
  root may use the socket; limits and the response format are specified.
- **Session resumption and per-request checks** (Codex). Session tickets are
  disabled; every protected request checks the certificate itself.
- **Pinning contract** (Codex) and **CA in the chain** (Antigravity).
  Specified, including the appended-CA case and refused redirects.
- **Server rotation and SNI** (Codex), **server names and IP SANs** (both).
  `GetCertificate` is the only source; `control.server_names` and
  `control.ca_key_file` are configuration; IPs go in `IPAddresses`.
- **Production layout** (Codex). Credentials must be replaceable by the agent
  user; Bart chose `/var/lib/sorry-portainer-agent` for config, credentials
  and state.
- **Clock skew** (both). 15-minute backdating, CA cap, clock diagnostics.
- **Token on the command line** (Codex). The token is read from stdin or a
  file only; the environment variable is gone.
- **Legacy certificates** (Codex). Legacy leaves have both usages and ten-year
  lifetimes; `server pki reset` replaces the whole PKI rather than importing
  them (Antigravity suggested a `hosts import` command, not taken for that
  reason). The `host-<id>` DNS fallback goes.
- **Wire formats and encoding** (Antigravity). Request, response and error
  shapes are specified; base64 is unpadded.
- **Fleet size and rotation writes** (Codex). The supported size, write
  frequency, jitter and benchmarks are in [Performance](#performance).

## References

- `docs/design/architecture.md` — relay, the certificate allowlisting item,
  Phase 3 durable administration.
- `docs/design/stack-secrets.md` — production agent layout item.
- `internal/provision/provision.go` — current CA and certificate generation.
- `internal/server/control.go` — control handler and certificate identity.
- `internal/relay/remote.go` — session registration.
- `internal/secrets/socket.go` — the unix socket pattern the admin socket follows.
