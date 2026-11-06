# Editable stack environment — Design

Status: released in v0.2.5 (deployed 2026-11-07); one open question
Owner: Bart
Last updated: 2026-11-07

## Implementation status

Tracking the gap between this design and the materialized implementation.
Tick items as they land and move them to Done.

### Done

- [x] **Decision — visible and editable.** Bart (2026-11-07): "These are ENV
  variables. Not secrets, the point of having them is that you can reload
  them and show them without issue. Please make them editable and visible."
  Secrets belong in stack secrets.
- [x] **Decision — recreate on save.** Bart (2026-11-07): when a save changes
  the environment of a stack that is up, the stack is recreated.
- [x] **Agent — environment per revision.** `internal/stacks/stacks.go`
  (`environment`, `writeRevision`, `checkEnvironment`, `Save`).
- [x] **Protocol and master.** `protocol.StackSaved`; `StackSave.Environment`
  without `omitempty`; relay, admin socket and web API saves on the stack
  operation timeout; the admin log says "environment kept" or the entry
  count.
- [x] **Dashboard.** `src/stack-api.ts` (`parseEnvironment`,
  `formatEnvironment`), the Environment field in `src/stacks-ui.tsx`.
- [x] **CLI.** `cmd/sorry-portainer`.
- [x] **Tests.** `internal/stacks/environment_test.go`,
  `TestRealPodmanEnvironmentChangeRecreates` (real podman-compose 1.0.6: the
  container sees the new value after the save), relay, admin, web API, CLI
  and `tests/stack-api.test.mjs`. Mutation-checked: always applying, ignoring
  the desired state, and clearing on a missing environment each fail a test.
  In the dev dashboard on 2026-11-07: values shown, an edit recreated the
  running container with the new value, a malformed line was refused before
  saving.

### Outstanding

- [ ] **Open — compose saves.** Should saving a changed compose file recreate
  a running stack too, as Portainer's "Update the stack" does? Today it
  waits for Start. Ask Bart.

## Why this exists

A stack's environment (`env.json`) can only be set when the stack is created,
and nothing shows it afterwards. The jp1 import moves inline values into
`${KEY}` references, so after the import nobody can see or change them: a
reference to a value you cannot change is worse than the inline value it
replaced. Values are configuration, not secrets: stack secrets exist for
passwords and keys.

## Goals

- See a stack's environment, with values, in the dashboard and the CLI.
- Change, add and remove entries; each change is a new revision, like a
  compose change.
- A stack that is up picks the change up on save.

## Non-goals

- Per-service environment files. One environment per stack, as now.
- Multi-line values (see [Values](#values)).
- Protecting values: they are plain text on the agent host, in the master's
  memory while relayed, and in the browser.

## Storage

```
stacks/<name>/
  compose.yaml -> revisions/N.yaml     # the active revision
  revisions/N.yaml
  revisions/N.env.json                 # new: the environment of revision N
  env.json                             # older stacks: the environment of every
                                       # revision without its own file
```

- A save writes `N.yaml` and `N.env.json`, then moves the `compose.yaml`
  pointer. The pointer is still the single commit point, so a crash leaves
  either the old revision or the new one whole, and a revision's compose file
  and environment can never mismatch.
- The environment of revision N is `revisions/N.env.json`, else the
  stack-level `env.json`, else empty. Stacks created before this change
  (foundryvtt on jp1) keep working unchanged and get per-revision files from
  their next save. The stack-level file is not rewritten or deleted.
- Every revision keeps its own copy (at most 128 entries). Copying is cheaper
  than any scheme that shares files across revisions, and keeps each revision
  self-contained.

## Protocol

- `StackSave.Environment` loses `omitempty`. Absent or `null` keeps the
  current environment (empty for a create); an object, including `{}`,
  replaces it.
- `Stack.Environment` (Inspect only) carries the active revision's values.
  `List` leaves it out, so lists stay small.
- Saves answer `StackSaved`: the stack, plus `operation` (a
  `StackOperation`) when the save ran `up`. A failed `up` after a successful
  save is an answer with `operation.success: false`, not an error: the
  revision exists.
- `stacks.save` gets the stack operation timeout everywhere (relay, admin
  socket, web API), since it may run `up`.

## Values

- Keys match `[A-Za-z_][A-Za-z0-9_]*`. The agent now checks this; it did not.
- Values may not contain a newline or NUL. An env file and the dashboard's
  `KEY=VALUE` field could not show such a value back, and a round trip would
  change it. Compose files with multi-line values keep them inline.

## Recreating on save

Under the stack lock, after the new pointer is in place: if the environment
differs from the previous revision's and the desired state is up, the agent
runs `up` (the same code as Start). podman-compose hashes the compose file
after substituting the environment and recreates the project when the hash
changes, so `up -d` is enough. A stack that is down or was never started is
only saved.

## Dashboard

- The stack editor gets an "Environment" textarea under the compose file:
  `KEY=VALUE` lines, `#` comments and blank lines skipped, sorted by key on
  load. The same rules as the CLI's `--env-file`; errors name the line.
- The environment is sent only when the field was edited, so saving a
  compose change never touches it.
- "Save and start" uses the save's `up` result when there is one rather than
  running `up` again.
- A note under the field says a running stack restarts when its environment
  changes.

## CLI

- `stacks show` prints the environment after the compose file.
- `stacks update --env-file FILE` replaces the environment; without it the
  environment is kept.
- When a save ran `up`, its output is printed, and a failed `up` exits 1.

## Tests

- Agent: create writes `1.env.json`; update without environment carries the
  values over; update with `{}` clears them; Inspect returns them; a legacy
  stack's `env.json` applies to its old revisions and is carried into the
  next one; invalid keys and newline values are refused; a changed
  environment on a stack that is up runs `up` with the new values, an
  unchanged one or a stack that is down doesn't; the `up` failure is in the
  answer and the revision stays.
- Relay, admin socket and web API: `null` versus `{}` reach the agent intact;
  saves use the long timeout.
- Dashboard: parsing (comments, duplicates, invalid keys), sending the
  environment only when edited.
- CLI: `show` prints values; `update --env-file`; the save's `up` output and
  exit code.

## References

- `internal/stacks/stacks.go`: `Save`, `operateLocked`.
- `docs/design/admin-cli.md`: the CLI this extends.
- `docs/design/stack-secrets.md`: where secrets go instead.
