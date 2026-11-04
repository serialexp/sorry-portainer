set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

root := justfile_directory()
dev_dir := root / ".dev"
bin_dir := dev_dir / "bin"
state_dir := dev_dir / "state"
units_dir := home_directory() / ".config/systemd/user"

# Build all Go commands without Git VCS stamping (this repository uses Wheat).
# The server embeds the dashboard, so the dashboard is built first.
build: build-ui
    go build -buildvcs=false -o {{bin_dir}}/sorry-portainer-server ./cmd/sorry-portainer-server
    go build -buildvcs=false -o {{bin_dir}}/sorry-portainer-agent ./cmd/sorry-portainer-agent

# Build the SolidJS dashboard.
build-ui:
    pnpm build

# Build and install the sorry-portainer agent in the current user's local bin directory.
install-spa:
    mkdir -p "{{home_directory()}}/.local/bin"
    go build -buildvcs=false -o "{{home_directory()}}/.local/bin/sorry-portainer-agent" ./cmd/sorry-portainer-agent

# Create the development server state, start the server, and enroll the three
# local agents with one-time tokens. Idempotent: hosts that already have a
# credential are left alone, and agent stack state is always kept. State from
# before enrollment gets a new CA once (`pki reset`; old files are moved to
# pki-retired-*). Override PASSWORD when invoking this recipe; it is never
# committed.
dev-init PASSWORD="dev-admin-password-change-me": build
    #!/usr/bin/env bash
    set -euo pipefail
    server="{{bin_dir}}/sorry-portainer-server"
    agent="{{bin_dir}}/sorry-portainer-agent"
    state="{{state_dir}}"
    test "{{PASSWORD}}" != "dev-admin-password-change-me" || echo "warning: using the documented development password"
    mkdir -p "$state"
    # The state holds the CA key; the server refuses a directory others can read.
    chmod 700 "$state"
    if [[ ! -f "$state/server.json" ]]; then
      "$server" init --state-dir "$state" --admin-password "{{PASSWORD}}" --server-name 127.0.0.1 --control-listen 127.0.0.1:6243 --web-listen 127.0.0.1:6200
    elif ! jq -e '.control.server_names | length > 0' "$state/server.json" >/dev/null; then
      systemctl --user stop sorry-portainer-server.service || true
      "$server" pki reset --state-dir "$state" --server-name 127.0.0.1
    fi
    just install-dev-services
    systemctl --user start sorry-portainer-server.service
    for _ in $(seq 100); do
      "$server" hosts list --state-dir "$state" >/dev/null 2>&1 && break
      sleep 0.1
    done
    "$server" hosts list --state-dir "$state" >/dev/null
    for host in a b c; do
      dir="$state/agents/local-$host"
      [[ -f "$dir/agent.pem" ]] && continue
      mkdir -p -m 700 "$dir"
      replace=()
      [[ -f "$dir/agent.json" ]] && replace=(--replace)
      "$server" hosts token --state-dir "$state" --host "local-$host" \
        | "$agent" enroll --server-url wss://127.0.0.1:6243/control/agent --host-id "local-$host" --dir "$dir" --prefix "sorry-local-$host-" --token-file - "${replace[@]}"
      # Credentials from before enrollment, signed by the retired CA.
      rm -f "$dir/agent.crt" "$dir/agent.key"
      systemctl --user try-restart "sorry-portainer-agent-$host.service"
    done

# Write user-level systemd units pointing at this checkout. The agents get
# Podman's OCI hooks directory through CONTAINERS_CONF_OVERRIDE, so
# development never edits ~/.config/containers.
install-dev-services: build
    mkdir -p "{{units_dir}}"
    "{{bin_dir}}/sorry-portainer-agent" setup-hooks-conf --print > "{{state_dir}}/containers-hooks.conf"
    for template in server.service ui.service; do \
      sed -e 's#__ROOT__#{{root}}#g' -e 's#__BIN__#{{bin_dir}}#g' -e 's#__STATE__#{{state_dir}}#g' "{{root}}/deploy/systemd/sorry-portainer-$template" > "{{units_dir}}/sorry-portainer-${template%.service}.service"; \
    done
    for host in a b c; do \
      sed -e 's#__ROOT__#{{root}}#g' -e 's#__BIN__#{{bin_dir}}#g' -e 's#__STATE__#{{state_dir}}#g' -e "s#__HOST_ID__#local-$host#g" "{{root}}/deploy/systemd/sorry-portainer-agent.service.template" > "{{units_dir}}/sorry-portainer-agent-$host.service"; \
    done
    systemctl --user daemon-reload

start-server: build dev-init
    systemctl --user start sorry-portainer-server.service

start-a: build dev-init
    systemctl --user start sorry-portainer-agent-a.service

start-b: build dev-init
    systemctl --user start sorry-portainer-agent-b.service

start-c: build dev-init
    systemctl --user start sorry-portainer-agent-c.service

start-ui:
    systemctl --user start sorry-portainer-ui.service

start: build-ui dev-init
    systemctl --user start sorry-portainer-server.service sorry-portainer-agent-a.service sorry-portainer-agent-b.service sorry-portainer-agent-c.service sorry-portainer-ui.service

stop:
    systemctl --user stop sorry-portainer-ui.service sorry-portainer-agent-c.service sorry-portainer-agent-b.service sorry-portainer-agent-a.service sorry-portainer-server.service || true

restart: build-ui build dev-init
    systemctl --user restart sorry-portainer-server.service sorry-portainer-agent-a.service sorry-portainer-agent-b.service sorry-portainer-agent-c.service sorry-portainer-ui.service

status:
    systemctl --user --no-pager --full status sorry-portainer-server.service sorry-portainer-agent-a.service sorry-portainer-agent-b.service sorry-portainer-agent-c.service sorry-portainer-ui.service

logs service="sorry-portainer-server.service":
    journalctl --user -u {{service}} -n 100 --no-pager

clean-dev-services:
    systemctl --user disable --now sorry-portainer-ui.service sorry-portainer-agent-c.service sorry-portainer-agent-b.service sorry-portainer-agent-a.service sorry-portainer-server.service || true
    rm -f "{{units_dir}}/sorry-portainer-server.service" "{{units_dir}}/sorry-portainer-agent-a.service" "{{units_dir}}/sorry-portainer-agent-b.service" "{{units_dir}}/sorry-portainer-agent-c.service" "{{units_dir}}/sorry-portainer-ui.service"
    systemctl --user daemon-reload
