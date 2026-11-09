# Stack image updates — Design

Status: implemented, not released; open: two-step progress, wording for states the design does not cover
Owner: Bart
Last updated: 2026-11-10

## Implementation status

Tracking the gap between this design and the materialized implementation.
Tick items as they land and move them to Done.

### Done

- [x] **Decision — recreate scope.** Bart (2026-11-10): "Service + its
  deps". After the pull the agent runs `up -d --force-recreate <service>`,
  which in podman-compose 1.0.6 also recreates the services `<service>`
  depends on.
- [x] **Decision — when to check.** Bart (2026-11-10): "On page open + Check
  now". The agent caches each result; nothing checks in the background.
- [x] **Decision — the age shown.** Bart (2026-11-10): "Image build time".
  The row says "built X ago" from the new image's config `created`, not the
  push time the design mocks, which registries do not report.
- [x] **Decision — registry lookup.** Bart (2026-11-10): "Go library in
  agent". go-containerregistry, HEAD requests, Podman's `auth.json`.

- [x] **Agent — registry checker.** `internal/imagecheck`: HEAD per image
  reference, cached 15 minutes (failures too), at most 1024 entries, 4
  requests at a time, 15 seconds each, one request per reference however
  many checks ask; the build time fetched once per new digest.
- [x] **Agent — local images.** `podman.Client.LocalImages` (one `podman
  images`), `Pull` (`podman pull --quiet --`), and `ImageID` on
  `ProjectContainer`.
- [x] **Agent — `stacks.images` and `stacks.update`.**
  `internal/stacks/images.go`; wired in `cmd/sorry-portainer-agent`.
- [x] **Protocol, relay, web API.** `protocol.StackImages`,
  `StackUpdateRequest`; `Remote.StackImages` (45 s), `UpdateStackService`
  (stack operation timeout);
  `GET /api/hosts/{host}/stacks/{name}/images[?force=1]`,
  `POST /api/hosts/{host}/stacks/{name}/update`.
- [x] **Dashboard.** `src/image-updates.ts` (API and wording),
  `src/image-update-store.ts`, the Services header and rows in
  `src/stacks-ui.tsx`. The update's output shows with the page's other
  operation results, as start and stop do; the design's "Last operation"
  card belongs to the body redesign in TODO.md.
- [x] **Tests.** `internal/imagecheck` (cache, failures, concurrency and
  sharing, eviction, an in-process registry with a multi-platform index),
  `internal/stacks/images_test.go` (every status, scaled services, update
  paths), `TestRealPodmanUpdateRecreatesOnlyTheService` (real
  podman-compose 1.0.6; fails without `--force-recreate`), web API,
  `internal/podman/pull_test.go`, `tests/image-updates.test.mjs`. In the
  dev dashboard on 2026-11-10: alpine:latest showed "Newer latest · built 3
  days ago", Update pulled and recreated `app` only, and the page then
  showed "Up to date".
- [x] **Extended — the update checks its result.** podman-compose 1.0.6
  exits 0 when `podman run` fails, so the update fails unless every
  container of the service runs, from the pulled image.
- [x] **Extended — performance.** `BenchmarkImagesLargeStack`: 200
  services, 5000 local images, registry answers cached: 1.8 ms and 13k
  allocations per check (Ryzen 9 9900X). Local tags are parsed only when a
  service has no container (7.2 ms and 76k allocations before).
- [x] **Extended — narrow screens.** The services table scrolls sideways
  instead of cutting off the Update column.

- [x] **Decision — TTL.** Bart (2026-11-10): 15 minutes, confirmed.
- [x] **Decision — "built", not "pushed".** Bart (2026-11-10): expected.
- [x] **Extended — failure kinds.** Bart (2026-11-10): an auth failure
  is different from an unreachable registry. The checker classifies a
  failure by HTTP status (`denied` for 401/403, `not_found` for 404,
  `unreachable` otherwise) and `ServiceImage.failure` carries it. The row
  says "No access", "Tag not found" or "Couldn’t check"; the header says
  "{registry} refused access", "Tag {tag} not found on {registry}" or
  "Couldn’t reach {registry}". The wording is ours, not the design's.

### Outstanding

- [ ] **Open — progress while updating.** The design shows "Pulling new
  image…" then "Recreating container…"; the update is one request with no
  progress, so the row says "Updating…" throughout. Bart (2026-11-10)
  wants two steps; how the dashboard learns the step is undecided.
- [ ] **Open — states the design does not cover.** Images pinned by digest,
  local images (`localhost/…` or `build:`), and image names with variables
  in a stopped stack. They show a grey line for now ("Pinned by digest",
  "Local image", "Not checked"); Bart to confirm with the design agent,
  together with the failure wording below.
- [ ] **Defer — CLI.** `sorry-portainer stacks images` / `stacks update`.
  Bart (2026-11-10): "Fine" to leave out of v1.

## Why this exists

A stack that runs `docker.io/aeolun/homes:latest` keeps running the image
that was pulled when its container was created. Publishing a new image under
the same tag changes nothing on the host, and the stack page gave no sign
that the tag had moved. The design's StackDetail board shows the image's
state per service and an Update action; this doc is the contract for how the
agent finds out and how it updates.

## Goals

- Show per service whether the image it runs is still what its tag points
  to in the registry.
- Update one service: pull its image, and when the stack is up, recreate the
  service (and what it depends on).
- Cost almost nothing at the registry: Docker Hub does not count manifest
  HEAD requests toward its pull limit, so a check is HEAD only, and the
  agent caches the answers.

## Non-goals (v1)

- Background checks or notifications. A check happens when the stack page
  opens or on Check now.
- Updating every service at once, or the stack list showing updates.
- Images built from a `build:` section.

## How a check works

The dashboard asks `GET /api/hosts/{host}/stacks/{name}/images` when the
stack page opens and with `?force=1` on Check now. The agent:

1. Reads the stack's Compose file and the project's containers (one
   `podman ps`).
2. Picks each service's image reference: the container's image as Podman
   reports it (fully qualified, variables already resolved), or for a
   service without a container the Compose file's `image:`.
3. Classifies references that are not checked: `@sha256:` pins ("pinned"),
   `localhost/` images and services without `image:` ("local"), and a
   Compose `image:` with `${…}` in a stack without containers ("not
   checked", the variables are resolved only when Compose runs).
4. Lists local images once (`podman images --format json`) for each image's
   ID, repo digests and Podman's pull digest.
5. Asks the checker for the remote digest of each distinct reference.
   Fresh cache entries (15 minutes) answer without a request unless forced;
   the rest are HEAD requests, at most 4 at a time, 15 seconds each.
6. Compares: a service is current when the remote digest is one of the
   repo digests of the image its container runs (for a service without a
   container, of the local image its reference names). Podman records both
   the manifest-list digest and the platform digest it pulled, so multi-arch
   tags compare correctly. A service whose image is not on the host at all
   is behind.
7. For a service that is behind, the checker fetches the new image's config
   for its build time, once per digest (one manifest GET and one config
   blob GET for the host's platform). These GETs do count toward Docker
   Hub's pull limit; they happen only when a tag has moved.

A failed HEAD keeps the last good answer, if any, and reports the error and
when the last good check was. The service is then "Couldn't check" and the
header says "Couldn't reach {registry} · last checked X ago".

The cache is keyed by the image reference and bounded (1024 entries, the
oldest dropped first). Concurrent checks of one reference share one request.

## How an update works

`POST /api/hosts/{host}/stacks/{name}/update` with `{"service": "app"}`.
Under the stack's lock, the agent:

1. Resolves the service's image reference as in a check and refuses pinned,
   local and unresolved references.
2. Runs `podman pull -- <reference>`; Compose's own `pull` ignores podman's
   exit code in 1.0.6.
3. When any container of the stack runs, runs `podman-compose -p <project>
   -f runtime-compose.yaml up -d --force-recreate <service>` with the
   stack's environment and secrets, as `up` does. podman-compose 1.0.6 does
   not recreate on `up -d <service>` alone (the config hash has not
   changed); `--force-recreate` takes the service and the services it depends
   on down and brings them up again. When the stack is stopped, the pull is
   all; the service uses the new image when the stack next starts.
4. Answers a `StackOperation` named `update`, whose output is the pull's and
   Compose's, shown under Last operation.

Added during implementation: between steps 3 and 4 the agent lists the
project's containers and fails the update unless every container of the
service runs and was created from the image the pull returned. podman-compose
1.0.6 exits 0 when `podman run` fails, so its exit code alone does not show
that the service came back.

The update forgets the cached check of the reference, so the next check sees
the new state. The update does not change the stack's desired state.

## Protocol

```go
type StackImagesRequest struct { Name string; Force bool }
type StackImages struct { Name string; Services []ServiceImage }
type ServiceImage struct {
	Service, Image, Status string // current, behind, unknown, pinned, local, unchecked
	RunningDigest, LatestDigest string
	LatestCreated, CheckedAt int64 // unix seconds
	Error string
}
type StackUpdateRequest struct { Name, Service string }
```

`stacks.images` runs on a 45-second relay timeout (several HEADs plus a
config fetch); `stacks.update` on the stack operation timeout.

## Performance

Per check: one `podman ps`, one `podman images`, one parse of the Compose
file, and at most one HEAD per distinct reference not fresh in the cache.
A stack page reopened within the TTL makes no registry request. Memory is
bounded by the cache size. Nothing runs between page opens.

## References

- Design: StackDetail board, `https://claude.ai/artifact/306ed412-5a62-4bff-b6dd-51bc04a64af4`.
- podman-compose 1.0.6 `compose_up`, `compose_pull`, `get_excluded`.
- go-containerregistry `pkg/v1/remote`, `pkg/authn/keychain.go`.
