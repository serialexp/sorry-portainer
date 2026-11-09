import { createEffect, For, Show } from "solid-js";
import { A, useLocation } from "@solidjs/router";
import { fleet, selectedHost, signOut } from "./fleet-store";
import { loadTokenCount, outstandingTokenCount } from "./enrollment-store";
import { enrollmentPath, hostPath, parseHostPath } from "./host-route";
import {
  BackIcon,
  BrandMark,
  ContainerIcon,
  GridIcon,
  ImageIcon,
  KeyIcon,
  ServerIcon,
  StackIcon,
  VolumeIcon,
} from "./icons";
import { SecretStoreSidebar } from "./secrets-ui";

// The dashboard's sidebar. Fleet pages list the hosts and enrollment; a
// host's pages show that host and its resources instead. A resource link
// stays highlighted on that resource's own pages (an image, a stack).

/** The fleet: all hosts, each host, and enrollment with its unused tokens. */
function FleetNav() {
  return (
    <nav class="side-nav" aria-label="Fleet">
      <div class="side-label">Fleet</div>
      <A class="side-link" href="/" end>
        <GridIcon />
        <span>All hosts</span>
        <span class="side-count">{fleet.hosts.length}</span>
      </A>
      <For each={fleet.hosts}>
        {(host) => (
          <A class="side-link side-host" href={hostPath(host.host_id, "containers")}>
            <span class="side-dot" aria-hidden="true"></span>
            <span class="side-host-name">{host.host_id}</span>
            <span class="side-host-role">{host.hostname}</span>
          </A>
        )}
      </For>
      <div class="side-divider" aria-hidden="true"></div>
      <A class="side-link" href={enrollmentPath}>
        <KeyIcon size={16} />
        <span>Enrollment</span>
        <Show when={outstandingTokenCount()}>
          <span class="side-badge" title="Unused enrollment tokens">
            {outstandingTokenCount()}
          </span>
        </Show>
      </A>
    </nav>
  );
}

/** The host whose pages are open, and links to its resources. */
function HostNav(props: { hostID: string }) {
  const selected = () => (props.hostID === fleet.selectedHost ? selectedHost() : undefined);
  return (
    <>
      <div class="side-host-block">
        <A class="side-back" href="/">
          <BackIcon />
          <span>All hosts</span>
        </A>
        <div class="side-host-card">
          <span class="side-host-icon" aria-hidden="true">
            <ServerIcon />
          </span>
          <div>
            <strong>{selected()?.host_id ?? props.hostID}</strong>
            <span>
              <Show when={selected()}>
                <span class="side-dot small" aria-hidden="true"></span>
              </Show>
              {selected()?.hostname ?? "Host not found"}
            </span>
          </div>
        </div>
      </div>
      <Show when={selected()}>
        <nav class="side-nav" aria-label="Resources">
          <div class="side-label">Resources</div>
          <A class="side-link" href={hostPath(props.hostID, "containers")}>
            <ContainerIcon />
            <span>Containers</span>
            <span class="side-count">{fleet.containers.length}</span>
          </A>
          <A class="side-link" href={hostPath(props.hostID, "images")}>
            <ImageIcon />
            <span>Images</span>
            <span class="side-count">{fleet.images.length}</span>
          </A>
          <A class="side-link" href={hostPath(props.hostID, "volumes")}>
            <VolumeIcon />
            <span>Volumes</span>
            <span class="side-count">{fleet.volumes.length}</span>
          </A>
          <A class="side-link" href={hostPath(props.hostID, "stacks")}>
            <StackIcon />
            <span>Stacks</span>
            <span class="side-count">{fleet.stacks.length}</span>
          </A>
        </nav>
      </Show>
    </>
  );
}

export function Sidebar() {
  const location = useLocation();
  const route = () => parseHostPath(location.pathname);
  createEffect(() => {
    if (fleet.phase === "ready" && !fleet.demo) void loadTokenCount();
  });
  return (
    <aside class="sidebar">
      <A class="brand" href="/">
        <BrandMark />
        <span>sorry,portainer</span>
      </A>
      <Show when={route()} fallback={<FleetNav />}>
        {(current) => <HostNav hostID={current().hostID} />}
      </Show>
      <div class="sidebar-bottom">
        <SecretStoreSidebar />
        <div class="sidebar-status">
          <span class="pulse" aria-hidden="true"></span>All systems nominal
        </div>
        <div class="sidebar-footer">
          <span class="avatar" aria-hidden="true">
            B
          </span>
          <Show when={fleet.demo}>
            <span class="demo-badge">Demo</span>
          </Show>
          <button class="text-button" type="button" onClick={() => void signOut()}>
            Sign out
          </button>
        </div>
      </div>
    </aside>
  );
}
