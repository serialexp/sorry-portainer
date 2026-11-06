# Caddy on Podman (jp1 cutover) — Design

Status: partial — decisions made, Podman 6 packages released and tried on jp1, phases 1 (v0.2.3) and 2 done; phases 3–4 outstanding
Owner: Bart
Last updated: 2026-11-07

## Implementation status

Tracking the gap between this design and what is deployed. Tick items as
they land and move them to Done.

### Done

- [x] **Survey of jp1's current proxy.** 2026-11-05; see [Today](#today).
- [x] **Decision — where Caddy runs.** Bart (2026-11-05): option A, a sorry-portainer stack under the agent user. Portainer has the same self-dependency and it has been fine.
- [x] **Decision — how Caddy reaches the dashboard.** Bart (2026-11-05): try pasta's host-loopback mapping with the master staying a host service on 127.0.0.1; if that doesn't work on Podman 4.9, run the master as a container on the `caddy` network.
- [x] **Decision — migration shape.** Bart (2026-11-05): big-bang. None of the stacks is uptime-sensitive.
- [x] **Experiment — rootless Caddy on Podman 4.9.** Run on jp1 under a throwaway account on 2026-11-05, then removed; [results](#experiment-results-2026-11-05).
- [x] **Decision — accounts.** Bart (2026-11-05): `sorry-portainer-master` for the server and `sorry-portainer-agent` for the agent, on every host. hel1's existing `sorry-portainer` account, with its rootless storage, is renamed rather than recreated.

- [x] **Decision — client addresses.** Bart (2026-11-05): option (3), Podman 6 with `rootless_port_forwarder = "pasta"`, built by us; see [Podman 6](#podman-6-2026-11-06).
- [x] **Podman 6 packages.** `serialexp/podman-ubuntu` on Gothab builds ten packages for 24.04 and 26.04; its smoke test checks, fresh and over Ubuntu's Podman, that a rootless published port sees an off-host client's address with pasta (CI, 2026-11-06). Release `v6.1.4-1`; it has two AppArmor bugs fixed in revision 2 (see [Podman 6 on jp1](#podman-6-on-jp1-2026-11-06)).
- [x] **Release `v6.1.4-2` on jp1.** 2026-11-06: revision 2 carries both AppArmor fixes and installed over the hand-fixed `v6.1.4-1` without conffile prompts; pins in `deploy/rsansible/vars/podman.yml`.
- [x] **Decision — dashboard route.** Bart (2026-11-06): map host loopback (`--map-host-loopback 169.254.1.3`) for the agent user, accepting that every agent-user container can reach the host's 127.0.0.1, "fine as long as we don't host anything sensitive or directly accessible on the hosts". On jp1 that is today mysqld (3306/33060, Ubuntu's mysql-server 8.0, one database `spacetraders`, no connections seen).
- [x] **Agent-user containers.conf.** 2026-11-06: `setup` writes `rootless_port_forwarder = "pasta"` on Podman 6, and with `--map-host-loopback ADDR` (Podman 5 or later; the role's `sorry_portainer_agent_host_loopback`) `pasta_options = ["--map-host-loopback", ADDR]`, as the agent user through `setup-containers-conf`. A forwarder change is refused while the user's containers run, as Podman requires. `setup --dry-run` as root compares the drop-in, so the role reruns setup when it is stale. Pointing Caddy at `169.254.1.3:6200` is phase 3.
- [x] **Agent setup installs a complete Podman.** 2026-11-06: apt satisfies `crun | runc` with Docker's `runc` (`containerd.io`) on 24.04, and 24.04's podman doesn't recommend netavark, so it falls back to CNI. `setup` now installs whichever of podman, podman-compose, crun, netavark, aardvark-dns and passt is missing. With our packages all but podman-compose are already there.
- [x] **Hosts install the pinned packages.** 2026-11-06: the agent role installs the `v6.1.4-2` packages from `vars/podman.yml` (`scripts/pins.sh` in podman-ubuntu, which now pins each version) when dpkg reports other versions.
- [x] **Phase 1 — account rename, in code.** 2026-11-06: `setup` defaults to `sorry-portainer-agent`, and takes the user's home from passwd instead of assuming `/home/USER`. The server role renames a `sorry-portainer` whose home is the state directory to `sorry-portainer-master`. The agent role renames a `sorry-portainer` in `/home/sorry-portainer` to `sorry-portainer-agent`. It refuses unless that user's Podman holds nothing, resets the storage, stops its user manager, moves the home, and renames the group and the subordinate ranges, which `usermod -l` leaves alone. Bart chose (2026-11-06) to move hel1's home over keeping it.
- [x] **Phase 1 — hosts migrated.** 2026-11-06, release v0.2.3 through the roles: jp1's master runs as `sorry-portainer-master`. hel1's agent runs as `sorry-portainer-agent` in `/home/sorry-portainer-agent` on Podman 6.1.4 with pasta forwarding, and is online. A second run changes nothing. Two role bugs were found and fixed on the way. rsansible's `set_fact` turned `"26.04"` into a number. Its lineinfile wants `${1}`, not `\1`, so hel1's subuid/subgid ranges were briefly a literal `\1`; they were restored by hand to `100000:65536` (backups in `/etc/sub[ug]id.sp-backup`).
- [x] **Phase 2 — agent on jp1.** 2026-11-06, through the roles with `sorry_portainer_agent_host_loopback: 169.254.1.3` in jp1's inventory entry. jp1 runs `sorry-portainer-agent` (uid 1000, ranges `100000:65536`, lingering) with pasta forwarding and the host-loopback drop-in, enrolled and online next to hel1; Docker and its 21 containers untouched. A rootless container on a bridge network reached the host's 127.0.0.1:3306 at 169.254.1.3, and not at 169.254.1.2. A second run changes nothing. Check mode on a host without the agent failed twice, both fixed in the role: the setup dry run needs the downloaded binary, and enrolling as the not-yet-created account fails even when check mode would skip it.
- [x] **Experiment — Podman 6 on jp1.** 2026-11-06, `v6.1.4-1` plus the two fixes applied by hand; [results](#podman-6-on-jp1-2026-11-06). Client addresses, caddy-docker-proxy and host loopback (`--map-host-loopback`) all work.

### Outstanding

- [ ] **Experiment — reboot.** Question 5 (lingering brings Caddy and the stacks back after a reboot) needs a jp1 reboot; do it in the switch window.
- [ ] **Gothab apt repository.** Replace the pinned `.deb` downloads with a sources entry once Gothab has apt repositories (REQ-55 in serialexp/gothab).
- [ ] **CLAUDE.md native-packages rule.** The project rule still says agents use Ubuntu's native packages; it should name the podman-ubuntu packages. Waiting on Bart.
- [ ] **Phase 3 preparation.** Survey done 2026-11-07 ([Phase 3 survey](#phase-3-survey-2026-11-07)); waiting on Bart's choices there (stack import route, inline credentials, disk), then converted stacks, the Caddy stack, the volume move script and a dry run with Docker still up.
- [ ] **Phase 3 — big-bang switch.** In one window: stop the Docker stacks; copy volumes and `caddy_data`; start Caddy and every stack as sorry-portainer stacks with `jp1-` names; point the master's web listener at 127.0.0.1 (or containerize it).
- [ ] **Phase 4 — retire Docker on jp1.** Portainer, the label container and the role's Docker tasks go; Docker is removed once the switch has held.

## Why this exists

Bart wants jp1 off Docker within days, so that sorry-portainer and Podman run
everything there. Most of that is moving stacks one at a time. Caddy is the
exception. Every public site on jp1 goes through one caddy-docker-proxy
container that reads Docker labels, and it owns ports 80, 443 and 8080. So it
has to be designed before any site can move, and it can't simply be moved
the way the other stacks are.

## Today

`caddy-proxy-caddy-1` (stack `caddy-proxy`, managed by Portainer) runs
`lucaslorentz/caddy-docker-proxy:ci-alpine`:

- publishes 80, 443 and 8080 on every address;
- mounts `/var/run/docker.sock` and the `caddy-proxy_caddy_data` volume
  (certificates and the ACME account) at `/data`;
- `CADDY_INGRESS_NETWORKS=caddy`; Docker network `caddy`, 172.20.0.0/16,
  gateway 172.20.0.1.

The configuration comes entirely from labels on 15 sites:

- **Plain `reverse_proxy {{upstreams N}}`:** crossnotifier, superchat
  marketing, archive and server (the server on `superchat.win:8080`),
  binschema, homes, portainer, shipbreaking, timestables.
- **Keepalive transport options:** relay, and Foundry twice (terra and foundry).
- **`tls.protocols`:** the superchat sites.
- **Route blocks with named upstreams:** groovewhale (`media-http:8081`,
  `realtime-core:8080`).
- **Literal host address:** sorry-portainer (`172.20.0.1:6200`, the label
  container).

Two containers also publish ports directly: superchat-server on 6465 and
6466, and relay on 4042.

caddy-docker-proxy supports Podman through Podman's Docker-compatible API
socket. `CADDY_DOCKER_NO_SCOPE=true` is its documented setting for Podman.
`CADDY_DOCKER_SOCKETS` takes several sockets.

## Constraints

- **Rootless networking.** The agent runs stacks as one rootless user. That
  user's container networks live in its own network namespace, so the host
  can't reach container IPs, and `{{upstreams}}` resolves to IPs only reachable
  from inside that namespace. Caddy has to sit on the same rootless network
  as the sites, or the sites have to publish ports.
- **Privileged ports.** `net.ipv4.ip_unprivileged_port_start` is 1024 on jp1.
  Rootless Caddy on 80/443 needs that lowered to 80, or a forward from 80/443
  to unprivileged ports.
- **Client addresses.** Podman 4.9 uses slirp4netns for rootless networking by
  default, and its default port handler (rootlesskit) hides the client's
  address. pasta keeps it, and `passt` is in 24.04 (git 2024-02-20). Sites
  that log or rate-limit by client address, and the dashboard's sign-in
  limiter through `trusted_proxies`, depend on this.
- **Socket trust.** Today Caddy holds the Docker socket, which is root on the
  host. On Podman it would hold the agent user's socket, which controls that
  user's containers. Still broad, but no longer root.
- **Certificates.** `caddy_data` holds certificates for 15 names. Copying it
  avoids reissuing them all at once and hitting ACME rate limits.
- **Versions.** jp1 is Ubuntu 24.04: Podman 4.9.3 and podman-compose 1.0.6.
  hel1 has Podman 5.7.0 and pasta as the default.

## Where Caddy runs

- **(A) A sorry-portainer stack under the agent user.** caddy-docker-proxy
  joins a rootless network `caddy`, with the agent user's
  `podman.sock` mounted. Sites keep their labels and `{{upstreams}}` unchanged.
  - Needs: the port sysctl, pasta (configured, not the 4.9 default), and
    `CADDY_DOCKER_NO_SCOPE`.
  - Caddy can be managed from the dashboard. If an operator stops it from
    the dashboard, every site goes down, including the dashboard itself.
- **(B) Like A, but outside the agent's prefix.** The same container, owned
  by a systemd unit (a Quadlet or a unit running `podman run`) that the role
  installs.
  - The agent can't stop it, because the name lacks `jp1-`, and the
    dashboard doesn't depend on itself.
  - The cost is a second way containers get onto the host.
- **(C) Caddy on the host, with sites publishing ports.** Stock Caddy as a
  system service on 80/443, with real client addresses and no sysctl. Each
  site publishes a port on 127.0.0.1.
  - Labels give way to a Caddyfile kept in the role or on the master.
  - `{{upstreams}}` and the route blocks become fixed ports, and every new
    site needs a Caddy change besides its stack.

## The dashboard

The master stays a host service, so Caddy has to reach a host listener from
its rootless namespace.

- **Host loopback through the namespace.** The server listens on
  127.0.0.1:6200, and Caddy uses pasta's host-loopback mapping or slirp4netns
  `allow_host_loopback`. Whether 4.9 and this passt version support it is an
  experiment question.
- **A listener on another host address.** For example a dummy interface or a
  private address that the namespace can route to. This is
  `host.containers.internal` in Podman's terms.
- **With option C,** Caddy is on the host and proxies to 127.0.0.1:6200
  directly.

Each of these makes the role's label container and the Docker bridge
lookup unnecessary.

## Accounts on jp1

The server role created `sorry-portainer` as a system account: uid 998, home
`/var/lib/sorry-portainer` (the 0700 server state directory), no subuid or
subgid ranges. The agent's `setup` expects to create that account with
`/home/sorry-portainer`, and rootless Podman stores images and volumes under
its home. Running `setup` on jp1 as things are would put Podman storage in
the server's state directory, or fail. Options:

- Rename the server's account (for example to `sorry-portainer-server`) on
  every master. This is a role change plus a `chown` of the state directory.
- Give the agent a different account name on hosts that also run the
  master. `setup --user` already exists, but the role fixes the name because
  rsansible doesn't template `become_user`.

## Migration

- **Per stack, behind Docker Caddy.** Docker Caddy keeps 80/443. A migrated
  stack publishes its port on the caddy bridge gateway (172.20.0.1), and a
  Docker label container points Caddy at it, the same way the dashboard works
  today. Once everything has moved, Podman Caddy takes over the ports in one
  short switch, with the certificate data copied.
  - Each site has a small cutover and can be rolled back on its own.
  - Every stack gets edited twice, once to publish its port and once to go
    back to labels.
- **Big-bang.** Stop Docker, start Podman Caddy and every migrated stack in
  one maintenance window.
  - Labels are written once.
  - A single failure takes every site down until it's fixed or Docker is
    started again.

## Experiment

Before any decision is final, on jp1 (24.04, Podman 4.9.3) under a throwaway
rootless account:

1. caddy-docker-proxy on a rootless network with the user's socket and
   `CADDY_DOCKER_NO_SCOPE=true`: does it see labels and resolve
   `{{upstreams}}`?
2. pasta as `rootless_network_cmd` on 4.9.3 with 24.04's passt: does a
   published port keep the client's address? Does the slirp4netns default
   lose it?
3. Can a rootless container reach a host 127.0.0.1 listener, through pasta's
   loopback mapping or slirp4netns `allow_host_loopback`?
4. Does publishing on 172.20.0.1 from rootless Podman work while Docker owns
   that bridge (the per-stack path)?
5. Does lingering plus a user unit bring Caddy back after a reboot, before
   the sites need it?

## Open questions for review

- Is the dashboard allowed to depend on a Caddy the dashboard can stop
  (option A)?
- Are labels worth keeping (A/B), or is a central Caddyfile acceptable (C)?
- Should port 8080 (superchat.win:8080) and the directly published ports
  (6465, 6466, 4042) stay as they are?

## References

- `deploy/rsansible/roles/sorry-portainer-server/tasks/caddy.yml`: today's
  label container.
- caddy-docker-proxy README: `CADDY_DOCKER_NO_SCOPE`, `CADDY_DOCKER_SOCKETS`,
  `CADDY_INGRESS_NETWORKS`.
- `docs/design/architecture.md`: the agent's prefix ownership model.

## Decisions (2026-11-05)

Bart chose option A for Caddy. The dashboard depending on a Caddy the
dashboard can stop is the same arrangement Portainer has had all along. For
the dashboard path the first choice is host loopback through pasta, with the
master in a container as the fallback. The switch is big-bang, so the
per-stack path, with its double edit of each stack, is dropped. The accounts
become `sorry-portainer-master` and `sorry-portainer-agent` everywhere.
hel1's account is renamed in place.

## Experiment results (2026-11-05)

Setup: jp1, Ubuntu 24.04, Podman 4.9.3, crun, netavark 1.4, passt
2024-02-20. A throwaway rootless user with `default_rootless_network_cmd =
"pasta"`, and Docker running alongside. Removed afterwards. Podman, passt,
crun, netavark and aardvark-dns stay installed.

1. **caddy-docker-proxy on the rootless socket works.** It ran with the
   user's `podman.sock` mounted as `/var/run/docker.sock`,
   `CADDY_DOCKER_NO_SCOPE=true` and `CADDY_INGRESS_NETWORKS=caddy` on a
   rootless network `caddy`. It read labels from other containers and
   resolved `{{upstreams 80}}`; the routed request returned 200.
2. **Client addresses are lost on bridge networks.** On pasta's default
   network a published port showed the real client address. On a bridge
   network (`caddy`) the port goes through `rootlessport`, and the container
   sees an internal address. `default_rootless_network_cmd` doesn't change
   that, so behind Caddy every site's `X-Forwarded-For` is `10.89.0.x`.
   Podman's fix (`rootless_port_forwarder = "pasta"`) is experimental, exists
   only in Podman 6.x, and needs passt 2026-05-07 or later. Neither 24.04
   (4.9) nor 26.04 (5.7) has it.
3. **Host loopback is reachable.** `--network pasta:--map-gw` reaches the
   host's 127.0.0.1 through the gateway address. Containers on a bridge
   network reach it at 10.0.2.2 once `containers.conf` has
   `network_cmd_options = ["allow_host_loopback=true"]` under `[engine]`.
   That is how Caddy on `caddy` reached a 127.0.0.1 listener (200). The
   master can therefore stay a host service on 127.0.0.1:6200, and the
   container fallback isn't needed.
4. Not needed: publishing on Docker's bridge (the per-stack migration was
   dropped).
5. Not tested: behaviour after a reboot.

Also tested: **Caddy on the host network** (rootless `--network host`),
with a site on `caddy` publishing `127.0.0.1:18091` and labelled
`caddy.reverse_proxy=127.0.0.1:18091`. The site saw the real client address
in `X-Forwarded-For`, and the published port could not be reached from
outside. caddy-docker-proxy logs an error until `CADDY_INGRESS_NETWORKS` is
set, because it can't find a network called `host`.

### The open choice: client addresses

- **(1) Bridge network, accept internal addresses.** Labels stay exactly as
  today (`{{upstreams}}`, named routes). Every site behind Caddy loses the
  visitor's address, in logs and in any rate limiting by address. The
  dashboard's sign-in limit then counts all visitors as one client, because
  every request arrives from Caddy.
- **(2) Caddy on the host network.** Real client addresses, and Caddy binds
  80/443 directly (still needs the port sysctl).
  - Each site publishes a port on 127.0.0.1 and its label names that port
    (`127.0.0.1:PORT`); `{{upstreams}}` and container-name upstreams such as
    groovewhale's `media-http:8081` give way to fixed ports.
  - The ports need choosing per site.
  - The dashboard is just `127.0.0.1:6200`.
- **(3) Podman 6 from upstream** with `rootless_port_forwarder = "pasta"`.
  Keeps labels and client addresses, but breaks the native-packages rule and
  depends on an experimental feature.

## Podman 6 (2026-11-06)

Bart chose option (3), with our own build. No supported repository has
Podman 6 for Ubuntu (24.04 has 4.9.3, 26.04 has 5.7.0; Kubic is
discontinued), so `serialexp/podman-ubuntu` builds it from pinned upstream
tags: podman 6.1.4, crun 1.30.1, conmon 2.2.1, netavark and aardvark-dns
2.1.0, passt 2026-10-02 (with pesto), catatonit 0.2.1, and the container-libs
configuration as `golang-github-containers-common`, `-image` and
`containers-storage`. They keep Ubuntu's package names and paths, so they
replace Ubuntu's in place, and use versions `<upstream>-1000sp<n>~ubuntu<rel>`
so they sort above Ubuntu's even at equal upstream versions.

Found while building:

- Podman 6's storage library applies a system `storage.conf`'s `graphroot`
  to rootless users. 26.04's `containers-storage` sets one, so after an
  upgrade every rootless user pointed at root's store. Our
  `containers-storage` ships upstream's file, which sets none.
- pasta gives a connection from one of the host's own addresses a
  link-local source (169.254.1.2) instead of the real one. Only off-host
  clients keep their address, which is what Caddy needs; a test from the
  host itself shows the link-local address.
- On CI, with a client in its own network namespace, `rootlessport` shows
  `10.89.0.2` and pasta shows the client's `192.0.2.2`, on both releases,
  fresh and over Ubuntu's Podman.

This goes against the project's rule of native Ubuntu packages for agents
(CLAUDE.md); that rule needs updating to name these packages.

## Podman 6 on jp1 (2026-11-06)

Installed `v6.1.4-1` on jp1 (Ubuntu 24.04) over Ubuntu's Podman 4.9.3, with
Docker running alongside; nothing was removed. A throwaway rootless user with
`rootless_port_forwarder = "pasta"`, removed afterwards.

Two AppArmor bugs, which CI couldn't see because its VMs confine nothing:

- **pasta ran under passt's profile.** Upstream installs `pasta` as a symlink
  to `passt`; AppArmor attaches profiles by resolved path, and passt's denies
  `rootless-netns-conn.pid`, so rootless networking failed outright.
  Ubuntu's package uses hard links; ours now does too.
- **Podman couldn't stop pasta.** Podman 6 sends the rootless-netns pasta
  SIGTERM on teardown, and upstream's pasta profile denies signals from
  Ubuntu's `podman` profile, so removing a container failed. Revision 2
  patches the profile (`signal (receive) peer={podman,unconfined}`); upstream
  passt has no fix as of 2026-11-06.

The CI smoke test now loads the packages' profiles (`SMOKE_APPARMOR=1`) so
the rootless checks run confined.

With both fixes applied by hand:

1. **Client addresses are kept.** From outside, a container on the bridge
   network `caddy` behind a published port saw the real client address.
2. **caddy-docker-proxy works** on the rootless socket as on 4.9
   (`CADDY_DOCKER_NO_SCOPE=true`, `CADDY_INGRESS_NETWORKS=caddy`): it read
   the labels, resolved `{{upstreams 8080}}`, and the site saw the real
   client address in `X-Forwarded-For`.
3. **Host loopback needs `--map-host-loopback`.** `host.containers.internal`
   (169.254.1.2) is the host's external address, so a listener on 127.0.0.1
   refuses. With `pasta_options = ["--map-host-loopback", "169.254.1.3"]`,
   containers on `caddy` and on pasta's default network reach the host's
   127.0.0.1 at 169.254.1.3. That opens every loopback service on the host to
   every container of that user, as `allow_host_loopback` did on 4.9.
4. Teardown is clean: pasta exits when the last container goes, no denials.

## Phase 3 survey (2026-11-07)
Read-only, with Docker running. Compose files were read structurally:
service keys, images, ports, mounts, networks and labels. Only the names of
environment variables were printed, never their values.

**Stacks.** Portainer runs 11 stacks from
`/var/lib/docker/volumes/portainer_data/_data/compose/<id>/[vN/]docker-compose.yml`:
caddy-proxy (1), foundryvtt (21), foundryvtt2 (25), superchat (73: server,
archiver, marketing), binschema (79), crossnotifier (80), timestables (81),
homes (85: app, worker, mysql, redis), shipbreaking (86), relay (95) and
groovewhale (97: postgres, media-http, realtime-core, web). Besides those, only
`portainer` and the `sorry-portainer-caddy` label container run.

**What each conversion needs:**

- **Fully qualified images.** jp1's `registries.conf` has no unqualified-search
  registries, so `mysql:8` and `aeolun/homes` must become `docker.io/…`.
- **No `container_name`.** relay sets `container_name: relay`. The agent only
  manages names with `jp1-`, so the line goes; podman-compose then names it
  `jp1-relay_relay_1`. Caddy's `{{upstreams}}` don't depend on names.
- **Credentials written inline.** foundry, foundryvtt2, superchat, crossnotifier
  and homes put credentials straight into `environment:` (no `${VAR}`).
  timestables and groovewhale read a `stack.env`. homes and shipbreaking
  reference `${…}` without a `stack.env`, so those values are in Portainer's
  database. The agent takes a stack's environment (`env.json`) only at
  creation. The master's secret store doesn't fit, because these apps read
  environment variables, not `/run/secrets` files.
- **`depends_on` conditions are ignored.** podman-compose 1.0.6 (jp1)
  translates `depends_on` into `--requires`, which only orders the starts.
  `condition: service_healthy` (homes, groovewhale) is dropped, so an app can
  start before its database is ready and relies on `restart: unless-stopped`.
  It does honour `init`, `pull_policy`, healthchecks, and service-name network
  aliases on every network. Those aliases keep groovewhale's routes
  (`media-http:8081`, `realtime-core:8080`, `web:8080`) resolving on `caddy`.
  Two stacks have a service called `app` on `caddy`, but nothing routes by
  that name.
- **Networks.** The external `caddy` network has to exist under the agent user
  first (`podman network create caddy`). Other networks become
  `jp1-<stack>_<net>`. groovewhale's `internal` stays `internal: true`.
- **Caddy itself.** Mount `/run/user/1000/podman/podman.sock` (the agent
  user's `podman.socket` is enabled) as `/var/run/docker.sock`, and set
  `CADDY_DOCKER_NO_SCOPE=true`. Ports 80 and 443 need
  `net.ipv4.ip_unprivileged_port_start=80`, which is 1024 today; 8080 doesn't.
- **Dashboard.** Through `--map-host-loopback`, the host sees Caddy's
  connections from 127.0.0.1, so `trusted_proxies` becomes `127.0.0.1/32`
  and the web listener `127.0.0.1:6200`. Every agent-user container then
  counts as a trusted proxy and could set `X-Forwarded-For` itself, so the
  sign-in limiter trusts all of them.

**Volumes and disk.** `/` has 24 GB free. Docker's in-use volumes:
homes_mysql-data 15 GB (11 GB `homes` schema, 3.8 GB in 9 binlogs),
foundryvtt2 3.9 GB, foundryvtt 275 MB, groovewhale postgres 65 MB, and a few
MB for the rest. The images the stacks use take about 4 GB.
Copying everything (about 19 GB) plus pulling the images (about 4 GB) would
leave under 1 GB. The data is owned by container uids (mysql 999, postgres 70,
foundry 421, caddy 0). Under the agent user, container uid `u` is host
`100000+u-1`, and 0 is host 1000. So a volume has to be streamed through
Podman's namespace (`podman volume import`, a copy) or moved and its owners
shifted (no copy, but rollback has to shift them back). Unused volumes:
`directus_*` (1 MB) and `captain--onedev` (1 GB).

**Importing stacks.** Neither the server's admin socket nor the agent's CLI
has stack commands, and the dashboard password stays unread. Choices:

- (A) Bart creates the stacks in the dashboard from prepared files.
- (B) The admin socket gets `stacks save/up/list --host`, so root on the
  master can script it. This is new code and a release.
- (C) Root writes the stack directories in the agent's on-disk format and
  brings them up through the agent. This bypasses the API.
