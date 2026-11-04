# Deploying with rsansible

`site.yml` installs the sorry-portainer master and its agents from a Gothab
release and enrolls each agent with a one-time token. Run it with
`~/.local/bin/rsansible`:

```bash
cd deploy/rsansible
rsansible validate site.yml
rsansible run -i inventory/production.yml site.yml --diff
```

## Inventory

`inventory/` is Wheat-ignored: it names real hosts and holds the admin
password. Copy `inventory.example.yml` to `inventory/production.yml`, and put
the password in `inventory/group_vars/all/secrets.yml` (mode 0600):

```yaml
sorry_portainer_admin_password: "at least 12 characters"
```

The roles need:

| Variable | Where | Meaning |
| --- | --- | --- |
| `sorry_portainer_master` | all | Inventory name of the master; agents get their tokens there. |
| `sorry_portainer_control_url` | all | `wss://HOST:9443/control/agent`, as agents reach the master. |
| `sorry_portainer_server_names` | master | Names on the control certificate; must include the URL's host. |
| `sorry_portainer_dashboard_host` | master | Name Caddy serves the dashboard under. |
| `sorry_portainer_caddy_network` | master | Docker network caddy-docker-proxy watches (default `caddy`). |
| `sorry_portainer_host_id` | agent | Host ID (default: the inventory name). |

## What the roles do

**sorry-portainer-server** (the master):

- creates the `sorry-portainer` system account and `/var/lib/sorry-portainer`
  (0700);
- installs the pinned server binary in `/usr/local/bin`;
- on the first run, runs `init` with the admin password on stdin, which creates
  the control CA;
- writes the whole `server.json` on every run;
- installs and starts `sorry-portainer-server.service`.

The control listener is public on `:9443`. The dashboard listens on the
gateway address of the Caddy network, port 6200, so only containers on that
network can reach it, and the network's subnet is the trusted proxy.
caddy-docker-proxy configures itself from container labels, so the role runs
`sorry-portainer-caddy`, a `pause` container on that network that only carries
the `caddy` and `caddy.reverse_proxy` labels.

**sorry-portainer-agent**:

- downloads the pinned agent binary into `/var/cache/sorry-portainer`;
- runs `setup` from there when `setup --dry-run` reports missing pieces, or
  when the installed binary is not the pinned one. `setup` installs Podman and
  podman-compose, the rootless `sorry-portainer` user with lingering, the
  binary and its unit.
- when `/var/lib/sorry-portainer-agent/agent.json` is missing, issues a token
  on the master with `hosts token` and enrolls with it; the token only travels
  on stdin, under `no_log`.
- starts `sorry-portainer-agent.service`.

A second run changes nothing.

## Upgrading

Gothab release assets have no by-tag URL. To move to a new release, copy each
binary's asset URL from `gt release view vX.Y.Z -R serialexp/sorry-portainer`
and its SHA-256 from that release's `SHA256SUMS` into `vars/release.yml`, then
run the playbook. The roles restart only the services whose binary changed.

Removing a host from the fleet is not part of the roles. Run
`sorry-portainer-server hosts remove --host ID` on the master; to enroll the
host again, delete its `agent.json` before the next run.
