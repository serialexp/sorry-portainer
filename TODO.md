# Follow-up

- Found during relay hardening (2026-09-24), narrowed 2026-11-05 (listener timeouts and SIGTERM handling now exist):
  - Server shutdown gives in-flight web requests 10 seconds; a stack operation that runs longer is cut off when the process exits and the agent sessions close with it.

- Expose actual Compose/Podman stack runtime status instead of the current `saved` metadata status. The stack list explicitly labels this limitation.
- Verify `podman-compose` successful exit implies the intended workload was created and started; the installed provider has previously printed pull errors while returning exit code 0. Do not equate operation success with running containers until this check exists.

- Found while making agents survive restarts and reboots (2026-09-25):
  - **Needs Bart's decision:** after a reboot, bringing a stack back also starts one-off services that had exited on purpose (Docker only restarts containers with a restart policy). Options in `docs/design/stack-secrets.md`.
  - `podman-compose up -d` on existing containers prints a "name already in use" error for each before it falls back to `podman start`; the stack output shown in the UI looks alarming although it worked.
  - The stack list still cannot tell whether containers are running (see the runtime-status item above); "starts after reboot" is the desired state, not the live one.

- Found while building stack secrets (2026-09-24):
  - podman-compose 1.0.6 `down` leaves `<project>_default` networks behind. Older test runs left several `sorry-podman-test-*` and `sorry-local-a-podman-smoke-*` networks on Bart's machine (not removed; not mine). The secrets live test removes its own.
  - Verify the hook with crun, and on Ubuntu 26.04 / Podman 5.x under the dedicated agent user.
  - UI: the sidebar container count and host page don't refresh after a stack start; "Save and start" on the edit page saves a new revision even when nothing changed; the secrets panel's delivery line doesn't refresh after an unlock push.
  - Stack deletion doesn't exist yet; when added it must delete the stack's secrets on the master and make the agent forget them.
  - Backups of the master's sealed secret store (`<state_dir>/secrets`) are undecided.

- Found while deploying to jp1 and hel1 (2026-11-05):
  - jp1's root filesystem is 94% full (about 4.6 GB free). Nothing was cleaned up; that needs Bart.
  - Not yet tested: a reboot of jp1. The dashboard listens on the Caddy network's gateway address, which exists only once Docker is up. The unit is ordered `After=docker.service` and restarts every 5 s if the bind fails, so it should recover either way. Check the journal after the next reboot.
  - Every release means copying asset URLs and checksums into `deploy/rsansible/vars/release.yml` by hand, because Gothab has no by-tag download URL. Either Gothab gets one, or a small script fills the file from `gt release view`.
  - The role doesn't remove hosts or the label container; removing a host is `hosts remove` by hand.

- From the Codex/Antigravity review (2026-11-05), deferred by Bart until after v0.2.0:
  - The control listener's failure log (`internal/server/control.go`) is keyed by the full remote address, so rotating IPv6 addresses evict real entries and escape the per-address limit. Key it by `clientPrefix` (/64) and add a global lines-per-minute budget.
  - `enroll --replace` and a running agent's renewer both write the staged key and bundle. `enroll` should take the agent directory lock and refuse while the agent holds it.
  - Any 503 sets the enrollment page to "unavailable", which hides Refresh, so a transient 503 leaves no retry.
  - Reported but not verified: repeated immediate renewals once the CA has under 30 days left; a 128-character host ID gets a 129-character default prefix.
  - Deployment note: the default web listen address `:8080` binds every interface.
