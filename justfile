set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

root := justfile_directory()
dev_dir := root / ".dev"
bin_dir := dev_dir / "bin"
state_dir := dev_dir / "state"
units_dir := home_directory() / ".config/systemd/user"

# Build all Go commands without Git VCS stamping (this repository uses Wheat).
build:
    go build -buildvcs=false -o {{bin_dir}}/sorry-portainer-server ./cmd/sorry-portainer-server
    go build -buildvcs=false -o {{bin_dir}}/sorry-portainer-agent ./cmd/sorry-portainer-agent

# Build the SolidJS dashboard.
build-ui:
    pnpm build

# Build and install the sorry-portainer agent in the current user's local bin directory.
install-spa:
    mkdir -p "{{home_directory()}}/.local/bin"
    go build -buildvcs=false -o "{{home_directory()}}/.local/bin/sorry-portainer-agent" ./cmd/sorry-portainer-agent

# Create local development CA, server certificate, and three agent bundles.
# Override PASSWORD when invoking this recipe; it is never committed.
dev-init PASSWORD="dev-admin-password-change-me": build
    test "{{PASSWORD}}" != "dev-admin-password-change-me" || echo "warning: using the documented development password"
    mkdir -p "{{state_dir}}"
    if [[ ! -f "{{state_dir}}/server.json" ]]; then \
      "{{bin_dir}}/sorry-portainer-server" init --state-dir "{{state_dir}}" --admin-password "{{PASSWORD}}" --server-name control.local --control-listen 127.0.0.1:6243 --web-listen 127.0.0.1:6200; \
    fi
    for host in a b c; do \
      if [[ ! -f "{{state_dir}}/agents/local-$host/agent.json" ]]; then \
        "{{bin_dir}}/sorry-portainer-server" agent --state-dir "{{state_dir}}" --create "local-$host" --prefix "sorry-local-$host-" --server-url "wss://127.0.0.1:6243/control/agent" --server-name control.local; \
      fi; \
    done
    just install-dev-services

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
