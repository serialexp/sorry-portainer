# sorry-portainer

`sorry-portainer` is a Portainer replacement for managing Podman across multiple
hosts. The project is greenfield. The backend is Go, the frontend is SolidJS
with pnpm, and Podman hosts connect to the central server through outbound
agents authenticated with mutual TLS. Agents are Podman-only: they target Ubuntu
24.04 and 26.04 native packages, run as a dedicated rootless user, require
Podman 4.7 or later, and use a pinned `podman-compose`; the stack-secret
workflow remains unimplemented pending an experiment.

## Version control: Wheat

This repository uses **Wheat**, not Git, as its native version control system.
Do not run Git commands to initialize, commit, branch, merge, reset, restore,
or otherwise manage project history. Do not create a `.git` repository.

Wheat is installed as `wheat`. It uses a separate content-addressed store and a
workspace-local `.wheat/` metadata directory. The workspace is an editable
materialization of an immutable Wheat view; the files on disk are not the
repository authority.

### Inspecting the workspace

Run these commands before and after substantive work:

```bash
wheat status                 # workspace and repository state
wheat changes               # active logical changes and revisions
wheat log                   # immutable revision history
wheat diff                  # workspace vs pinned materialized view
wheat labels                # repository-local labels
wheat frontier list         # descendant frontiers for the active label
wheat validate              # validate retained repository state
```

Use structured output for automation rather than parsing human prose:

```bash
wheat status --format json
wheat diff --format json
```

Wheat's JSON output is versioned (`format: wheat-command-result`, with a
`version` field). Expected failures have structured categories, codes, and
retryability where supported.

### Starting and capturing work

A logical change is the unit of work. The normal multi-file workflow is:

```bash
wheat change new -m "Short change title"
# edit files in the workspace
wheat add path/to/file-a path/to/file-b
# repeat `wheat add` for later edits to the same selected change
wheat status
wheat diff
```

`change new` selects a new logical change. `add` captures explicitly named
workspace paths into the selected change and creates an immutable revision.
Repeated `add` commands continue the selected change; they are not separate
Git commits. Use `--change CHANGE_ID` when deliberately targeting a different
active change.

For a single path, `change new` can capture the initial edit directly:

```bash
wheat change new \
  -m "Add configuration" \
  --path config.example.yaml \
  --value '...'
```

Prefer explicit paths. `wheat add --all` captures all non-ignored modified and
deleted tracked paths, but untracked files still need explicit path arguments
(or the relevant supported include option). Keep captures bounded and review
`wheat diff` before capturing broad changes.

The workspace's selected logical change and authoring basis are Wheat metadata;
do not copy internal change, revision, view, or envelope IDs into scripts when
a normal command can resolve them.

### Materializing and switching

After a revision is captured, the repository may advance while the workspace
remains pinned to its prior materialized view. Inspect the state first, then use:

```bash
wheat materialize          # move the workspace to its resolved effective view
wheat refresh               # refresh the active label binding/frontier selection
wheat switch LABEL          # activate another label
wheat switch LABEL --frontier VIEW_ID
```

Materialization is fail-closed. It refuses to overwrite workspace content that
has changed since Wheat recorded its exact observed value. Do not work around
that refusal with destructive commands. Inspect `wheat status` and `wheat diff`,
then preserve, capture, or explicitly resolve the local files before retrying.

A label is bound to an immutable view. Descendant views form a frontier. Zero
frontiers selects the label basis, one frontier is selected automatically, and
multiple frontiers require an explicit `--frontier` choice. A routine linear
workflow should normally have one frontier; unexpected multiple frontiers are a
Wheat bug or an intentional parallel-history situation and must not be silently
resolved by guessing.

### Conflicts and recovery

Wheat retains conflicts as durable repository state rather than temporary merge
files. Inspect them with:

```bash
wheat conflicts
wheat edits --revision REVISION_ID
wheat resolve --path path/to/file --value 'resolved content'
wheat resolve --path path/to/file --delete
```

Do not discard `.wheat/` or the external Wheat store to hide conflicts. If a
command reports stale state, an exact-preimage mismatch, or a pending recovery
operation, stop and inspect the structured error, status, conflicts, and
workspace contents. Retry only after the cause is understood.

### Repository initialization and remote synchronization

A new checkout/workspace is initialized with:

```bash
wheat init
```

Wheat's native remote protocol uses `wheat-server` URLs:

```text
wheat://host:port/owner/repo
wheat+tls://host:port/owner/repo
```

The available operations are:

```bash
wheat clone wheat+tls://host:port/owner/repo
wheat fetch
wheat publish
```

`fetch` refuses when local unpublished work would be overwritten. `publish`
uses the observed remote head and refuses stale or diverged publication unless
an explicitly reviewed force operation is required. Do not use force options to
paper over a conflict or stale state.

Git import/export is only a compatibility boundary when explicitly required:
`wheat import` can bootstrap from a Git repository and `wheat push` can project
a selected clean Wheat view to a Git branch. These are not the project's native
history workflow.

## Engineering rules

- Read the relevant code and current Wheat status before editing.
- Keep changes in focused logical changes; capture related edits together.
- Never delete or overwrite workspace files or `.wheat/` metadata to recover from
  an error without explicit operator instruction.
- Use focused automated tests for changed behavior, including failure and
  boundary cases. Run the relevant tests before reporting the work.
- Check performance for operations that scale with hosts, containers, logs, or
  streamed Podman data. Avoid per-item durable writes and unbounded buffering.
- Keep backend, agent, protocol, and frontend responsibilities in cohesive
  modules rather than growing a monolith.
- Do not read secret files or print credentials. Keep mTLS keys, admin passwords,
  and Podman registry credentials out of the repository. The stack-secret
  workflow is unimplemented pending its experiment.
- The supported native Wheat implementation currently targets Linux; do not
  silently assume macOS or Windows filesystem semantics.

When handing work to another agent, include the current Wheat logical change,
relevant `wheat status`/`wheat changes` output, files touched, tests run, and any
known stale-state or conflict condition.
