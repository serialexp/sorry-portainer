# Remote CLI over SSH — Design

Status: partial — phases 1 and 2 released in v0.2.4 and deployed; the jp1 import is outstanding
Owner: Bart
Last updated: 2026-11-07

## Implementation status

Tracking the gap between this design and the materialized implementation.
Tick items as they land and move them to Done.

### Done

- [x] **Decision — transport.** Bart (2026-11-07): the CLI reaches the
  master's admin socket over SSH, the way Docker's `ssh://` hosts work.
  Rejected: HTTPS with long-lived API tokens (a new credential type), and
  HTTPS with the admin password (the password would have to live on the
  operator's machine).
- [x] **Phase 1 — stack actions on the admin socket.** `stacks.list`,
  `stacks.inspect`, `stacks.save`, `stacks.up`, `stacks.down` and
  `stacks.restart` for one host, through the same relay calls as the web API
  (`internal/admin/server.go`). Per-action deadlines (`ActionTimeout`), and
  requests up to 4 MiB (see [Changes during implementation](#changes-during-implementation)).
  The operation is cancelled when the client goes away.
- [x] **Phase 1 — `sorry-portainer-server admin dial-stdio`.**
  `cmd/sorry-portainer-server/admin.go` and `admin.DialStdio`.
- [x] **Phase 1 — the `sorry-portainer` CLI.** `cmd/sorry-portainer`, with
  `--host` on every command that names a host.
- [x] **Phase 1 — tests.** `internal/admin/stacks_test.go`,
  `cmd/sorry-portainer/main_test.go`; see [Tests](#tests).
- [x] **Extended beyond the plan — a failed stack operation keeps its
  output.** The relay returned a failed Compose run as a bare
  `exit status N`, losing the output, for the web API as well
  (`internal/relay/agent.go` `operationResult`).
- [x] **Phase 2 — release workflow and role task.** The release workflow
  builds `sorry-portainer` too; the server role installs it to
  `/usr/local/bin/sorry-portainer` from `sorry_portainer_cli_binary_url` and
  `sorry_portainer_cli_binary_sha256`.

- [x] **Phase 2 — release and pin.** v0.2.4 released and pinned in
  `deploy/rsansible/vars/release.yml`, deployed on 2026-11-07 (a second run
  changes nothing). From the workstation, `--master ssh://root@jp1…`
  `hosts list` and `stacks list` for both hosts work, a missing stack exits
  1 and a usage error 2; `sorry-portainer hosts list` works on jp1 itself.
  Not yet exercised for real: create, update, up, down and restart (there is
  no stack deletion yet, so a test stack would stay behind); the jp1 import
  will exercise them.

### Outstanding

- [ ] **Phase 2 — jp1 import.** The phase 3 migration in `caddy-podman.md`
  creates its stacks with the CLI on jp1, against the local socket, so the
  environment values never leave jp1.
- [ ] **Defer — other admin actions.** Containers, images and stack secrets
  stay in the dashboard until there is a need.

## Why this exists

Moving jp1's 11 Portainer stacks into sorry-portainer means creating them
with their environment and bringing them up. The only way to do that today
is the dashboard, which needs the admin password and a person clicking
through every stack. Bart wants a CLI on his own machine instead.

The admin socket (`<state_dir>/admin.sock`) already gives root and the
server's account on the master authority over enrollment
(`hosts token|list|remove`). SSH to the master already proves that
authority. Carrying the socket's exchange over SSH therefore adds a remote
CLI without a new kind of credential.

## Goals

- From the operator's machine: list hosts, issue tokens and remove hosts, and
  list, show, create, update, start, stop and restart stacks on any enrolled
  host.
- No credential beyond the SSH key that already reaches the master.
- Stack environment values (`env.json`) are read from a file, sent once,
  never printed and never logged.
- The same commands work on the master itself, against the local socket.

## Non-goals (v1)

- Containers, images, volumes and stack secrets. The dashboard covers them.
- A long-lived connection or multiplexing. Each command is one SSH session
  and one exchange.
- Non-root SSH users on the master, beyond what the socket already allows:
  the server's account, `sorry-portainer-master`, has no shell.

## Transport

```
sorry-portainer --master ssh://root@jp1.example.com stacks list --host jp1
  └─ ssh -T -o BatchMode=yes root@jp1.example.com -- \
       sorry-portainer-server admin dial-stdio --state-dir /var/lib/sorry-portainer
         └─ connects to /var/lib/sorry-portainer/admin.sock
```

- The CLI runs the system `ssh`, so `~/.ssh/config`, the agent and known
  hosts apply as they do for any SSH. `BatchMode=yes` turns a password prompt
  into an error. `--ssh-command` (or `SORRY_PORTAINER_SSH`) replaces the
  `ssh` program; the tests use it.
- `dial-stdio` runs on the master as the SSH user. It applies the client
  side's existing check (the socket must be held by root, itself, or the
  state directory's owner) and copies bytes in both directions until the
  server closes. The server checks the peer as it does today: only root and
  its own account get an answer.
- `--master` falls back to `SORRY_PORTAINER_MASTER`. Without either, the CLI
  uses the local socket in `--state-dir` (default `/var/lib/sorry-portainer`).
- `--state-dir` passes through to `dial-stdio`. The remote
  `sorry-portainer-server` must be on the SSH user's `PATH`; it is in
  `/usr/local/bin`.

## Admin protocol

One JSON request and one JSON response per connection, as today. `Request`
gains:

```go
Stack string              `json:"stack,omitempty"` // inspect, up, down, restart
Save  *protocol.StackSave `json:"save,omitempty"`  // save
```

| Action | Needs | Answer | Deadline |
| --- | --- | --- | --- |
| `token`, `list`, `remove` | as today | as today | 5 s |
| `stacks.list` | `host_id` | `[]protocol.Stack` | 15 s |
| `stacks.inspect` | `host_id`, `stack` | `protocol.Stack` | 15 s |
| `stacks.save` | `host_id`, `save` | `protocol.Stack` | 15 s |
| `stacks.up` / `.down` / `.restart` | `host_id`, `stack` | `protocol.StackOperation` | 7 min |

- The deadlines sit just above the relay's own (10 s, and 6 min for stack
  operations), so the relay's error arrives before the socket gives up. The
  request itself must arrive within 5 s. The deadline for the rest of the
  exchange is set once the action is known.
- `MaxRequest` grows from 64 KiB to 2 MiB. The agent accepts a compose file
  up to 1 MiB and up to 128 environment entries, and JSON escaping can
  double a file's size. The decoder still reads through a `LimitReader`.
- Relay failures map onto the socket's codes: `unavailable` (host offline,
  busy or disconnected), `timeout`, and `failed` (the agent ran the
  operation and reported failure, with its message).
- `MaxConnections` grows from 4 to 8. A stack operation can hold its slot
  for minutes, and the role's `hosts token` must not wait behind an import.
- The log records the action, host and stack name, never a compose file or
  an environment value.
- `stacks.inspect` returns the compose file, which can hold credentials for
  stacks written before env.json was used (the dashboard's Inspect returns
  it too). Only root or the server's account sees it.

## CLI

```
sorry-portainer [--master URL] [--state-dir DIR] [--json] COMMAND
  hosts list
  hosts token HOST [--ttl 15m]   # as built: --host HOST
  hosts remove HOST             # as built: --host HOST
  stacks list   --host HOST
  stacks show   --host HOST NAME            # compose file, version, status
  stacks create --host HOST NAME --file compose.yaml [--env-file FILE]
  stacks update --host HOST NAME --file compose.yaml [--expect-version N]
  stacks up|down|restart --host HOST NAME
```

- `--env-file` takes `KEY=VALUE` lines (blank lines and `#` comments
  skipped, no quoting or interpolation) or a JSON object, chosen by its first
  character. Duplicate or invalid keys are errors. Values never appear in
  output, errors or logs.
- `update` without `--expect-version` reads the current version and saves
  against it. The agent still refuses if another save got in between.
- `up`, `down` and `restart` print the compose output and exit non-zero
  when the operation failed.
- `--json` prints the answer as received; the default is a short table or
  text.
- Exit codes: 0 success, 1 the operation failed or was refused, 2 usage.

## Security

- **Authority.** Whoever can SSH to the master as root can already do
  everything the admin socket allows, and more. The CLI moves no line.
- **Environment values** travel CLI → SSH → socket → relay (mTLS) → agent,
  and are stored in `env.json` (0600, agent user). They are on the
  operator's disk only if the operator's `--env-file` is. The jp1 import
  runs on jp1 for that reason.
- **No shell interpretation.** The CLI passes the remote command as separate
  arguments after `--`. The state directory is validated as an absolute
  path without spaces, so the remote shell's word splitting can't change it.

## Performance

One SSH session per command: about 0.1–0.3 s of handshake on a warm
connection (ControlMaster makes it cheaper). The jp1 import is 11 creates and
11 ups. Answers are bounded by `MaxResponse` (16 MiB); a stack list is a few
hundred bytes per stack. Nothing scales with history.

## Tests

- Admin server with a fake relay: each stack action passes the right host,
  stack and save through. Relay errors map to the right codes. A missing
  `stack` or `save` is refused. A 1 MiB compose file fits, and more than
  2 MiB is refused. A stack operation gets the long deadline and a list the
  short one. Logs never contain an environment value.
- `dial-stdio`: an exchange through pipes reaches a test socket. A socket
  held by another uid is refused.
- CLI: `--ssh-command` points at a test script that checks the arguments and
  connects to a test socket, so the whole chain runs without SSH. Env-file
  parsing, including JSON, comments, duplicates and invalid keys. Exit codes.
- A real exchange against the master on jp1 once it is deployed.

As built, the CLI tests drive commands through a fake client
(`cmd/sorry-portainer/main_test.go`): connection settings, usage errors
before any request, output, `--json`, exit codes and env-file parsing, with
a check that no value is printed. The SSH chain is tested in
`internal/admin`: `TestSSHClientEndToEnd` runs the test binary as the `ssh`
program, which checks its arguments and runs `DialStdio` against a real
admin server.

## Changes during implementation

- **4 MiB requests, not 2 MiB.** A 1 MiB compose file made entirely of
  characters JSON escapes (`"`) is 2 MiB plus the envelope, so 2 MiB refused
  a file the agent accepts. `MaxRequest` is now `protocol.MaxMessageSize`
  (4 MiB), the relay's own limit, so nothing the relay would carry is
  refused at the socket.
- **Environment is editable (2026-11-07,
  [stack-environment.md](stack-environment.md)).** `stacks update` takes
  `--env-file`, `stacks show` prints the values, and a save that brings a
  running stack up prints that output. The "set when it is created" rule
  above no longer holds.
- **`--host` for the hosts commands too.** `hosts token --host HOST` and
  `hosts remove --host HOST` rather than a positional `HOST`, matching
  `sorry-portainer-server hosts` and the stacks commands.

## References

- `internal/admin`: today's socket, client and peer checks.
- `internal/server/server.go` `hostOperation`: the web API's stack routes.
- `internal/relay/remote.go`: relay calls and their timeouts.
- `docs/design/agent-enrollment.md`, "Admin socket and CLI".
- `docs/design/caddy-podman.md`, "Phase 3 survey".
