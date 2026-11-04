import { createEffect, createSignal, For, Show, onMount, untrack } from "solid-js";
import { A, Route, Router, useLocation, useNavigate } from "@solidjs/router";
import { render } from "solid-js/web";
import type { Image, Volume } from "./fleet-api";
import {
  boot,
  filteredContainers,
  filteredHosts,
  filteredImages,
  filteredVolumes,
  fleet,
  selectedHost,
  setQuery,
  showHost,
  signIn,
  signOut,
  toggleContainer,
} from "./fleet-store";
import { EnrollmentPage } from "./enrollment-ui";
import { enrollmentPath, hostPath, parseHostPath } from "./host-route";
import { HostSecretWarnings, SecretStoreBanner } from "./secrets-ui";
import { StackEditor, StackList } from "./stacks-ui";
import "./styles.css";

function App() {
  const location = useLocation();
  // The URL decides which host is selected.
  createEffect(() => {
    const hostID = parseHostPath(location.pathname)?.hostID;
    if (fleet.phase !== "ready" || !hostID) return;
    // Retried when the host list changes, so a host that appears later loads.
    fleet.hosts.some((host) => host.host_id === hostID);
    untrack(() => void showHost(hostID));
  });
  onMount(() => void boot());

  return (
    <Show when={fleet.phase !== "booting"} fallback={<div class="loading-screen">Opening your Podman fleet…</div>}>
      <Show when={fleet.phase === "ready"} fallback={<Login />}>
        <Dashboard />
      </Show>
    </Show>
  );
}

function Dashboard() {
  const location = useLocation();
  const route = () => parseHostPath(location.pathname);
  const section = () => route()?.section;
  const enrolling = () => location.pathname === enrollmentPath;
  const selected = () => (route()?.hostID === fleet.selectedHost ? selectedHost() : undefined);
  const heading = () => pageHeading(location.pathname);
  return (
    <div class="app-shell">
      <aside class="sidebar">
        <div class="brand">
          <span class="brand-mark">✦</span>
          <span>
            sorry,<strong>portainer</strong>
          </span>
        </div>
        <A class="fleet-link" href="/">
          ← All hosts
        </A>
        <A class="fleet-link fleet-link-next" href={enrollmentPath} activeClass="active">
          Enrollment
        </A>
        <Show when={route()} fallback={<p class="sidebar-hint">Select a host to browse its resources.</p>}>
          <div class="selected-host">
            <p class="eyebrow">SELECTED HOST</p>
            <strong>{selected()?.host_id ?? route()?.hostID}</strong>
            <small>{selected()?.hostname ?? "Host not found"}</small>
          </div>
          <Show when={selected()}>
            <nav aria-label="Host resources">
              <A href={hostPath(fleet.selectedHost, "containers")} activeClass="active" end>
                Containers <span>{fleet.containers.length}</span>
              </A>
              <A href={hostPath(fleet.selectedHost, "images")} activeClass="active" end>
                Images <span>{fleet.images.length}</span>
              </A>
              <A href={hostPath(fleet.selectedHost, "volumes")} activeClass="active" end>
                Volumes <span>{fleet.volumes.length}</span>
              </A>
              <A href={hostPath(fleet.selectedHost, "stacks")} classList={{ active: section() === "stacks" }}>
                Stacks <span>{fleet.stacks.length}</span>
              </A>
            </nav>
          </Show>
        </Show>
        <div class="sidebar-footer">
          <div class="pulse"></div>All systems nominal
          <button class="text-button" onClick={() => signOut()}>
            Sign out
          </button>
        </div>
      </aside>
      <main>
        <header>
          <div>
            <p class="eyebrow">{heading().eyebrow}</p>
            <h1>
              {heading().title} <span>✦</span>
            </h1>
            <p class="subtitle">{heading().subtitle}</p>
          </div>
          <div class="header-actions">
            <label class="search-box">
              <span>⌕</span>
              <input
                value={fleet.query}
                onInput={(event) => setQuery(event.currentTarget.value)}
                placeholder={heading().search}
                aria-label="Search"
              />
              <kbd>⌘ K</kbd>
            </label>
            <div class="avatar">B</div>
          </div>
        </header>
        <div class="notice">{fleet.notice}</div>
        <SecretStoreBanner />
        <Show when={enrolling()}>
          <EnrollmentPage />
        </Show>
        <Show when={!route() && !enrolling()}>
          <Overview />
        </Show>
        <Show when={route() && !selected()}>
          <div class="resource-empty">
            {fleet.hosts.some((host) => host.host_id === route()?.hostID)
              ? "Loading host…"
              : "Host not found. Choose a host from the overview."}
          </div>
        </Show>
        <Show when={selected()}>
          <Show when={section() === "containers"}>
            <HostPage />
          </Show>
          <Show when={section() === "images"}>
            <ResourcePage kind="images" />
          </Show>
          <Show when={section() === "volumes"}>
            <ResourcePage kind="volumes" />
          </Show>
          <Show when={section() === "stacks"}>
            <Show when={route()?.action} fallback={<StackList />}>
              <StackEditor />
            </Show>
          </Show>
        </Show>
      </main>
    </div>
  );
}
/** The header text for the page at pathname. */
function pageHeading(pathname: string): { eyebrow: string; title: string; subtitle: string; search: string } {
  if (pathname === enrollmentPath)
    return {
      eyebrow: "FLEET / ENROLLMENT",
      title: "Enrollment",
      subtitle: "Hosts allowed to connect, and tokens for new ones.",
      search: "Search enrolled hosts",
    };
  const route = parseHostPath(pathname);
  if (!route)
    return {
      eyebrow: "OVERVIEW / PODMAN FLEET",
      title: "Hosts",
      subtitle: "Choose a host to browse its resources.",
      search: "Search hosts",
    };
  const eyebrow = `${route.hostID} / PODMAN`;
  const search = `Search ${route.section}`;
  switch (route.section) {
    case "images":
      return { eyebrow, search, title: "Images", subtitle: "Images available on this host's Podman installation." };
    case "volumes":
      return { eyebrow, search, title: "Volumes", subtitle: `Persistent storage owned by ${route.hostID}.` };
    case "containers":
      return { eyebrow, search, title: "Containers", subtitle: `Containers on ${route.hostID}.` };
    case "stacks":
      return {
        eyebrow,
        search,
        title: route.action === "new" ? "Create stack" : route.action === "edit" ? "Edit stack" : "Stacks",
        subtitle: `Compose projects saved on ${route.hostID}.`,
      };
  }
}
function Overview() {
  const navigate = useNavigate();
  return (
    <>
      <section class="panel">
        <div class="panel-heading">
          <div>
            <p class="eyebrow">YOUR FLEET</p>
            <h2>Podman hosts</h2>
          </div>
          <A class="add-button" href={enrollmentPath}>
            Enroll a host
          </A>
        </div>
        <div class="host-list">
          <For each={filteredHosts()}>
            {(host) => (
              <button class="host-row" onClick={() => navigate(hostPath(host.host_id, "containers"))}>
                <span class="host-icon">⌂</span>
                <span class="host-copy">
                  <strong>{host.host_id}</strong>
                  <small>
                    {host.hostname} · Podman {host.engine_version}
                    {host.swap_active ? " · swap active" : ""}
                  </small>
                </span>
                <span class="online">
                  <i class="dot green"></i>Online
                </span>
                <span class="chevron">›</span>
              </button>
            )}
          </For>
        </div>
      </section>
    </>
  );
}
function HostPage() {
  return (
    <>
      <Show when={selectedHost()}>
        {(host) => (
          <>
            <section class="host-info-grid host-info-card">
              <div>
                <span>HOSTNAME</span>
                <strong>{host().hostname}</strong>
              </div>
              <div>
                <span>PODMAN</span>
                <strong>{host().engine_version}</strong>
              </div>
              <div>
                <span>CONTAINER PREFIX</span>
                <strong>{host().prefix}</strong>
              </div>
            </section>
            <HostSecretWarnings host={host()} />
          </>
        )}
      </Show>
      <section class="panel resource-preview">
        <div class="panel-heading">
          <h2>Containers</h2>
          <span class="resource-count">{filteredContainers().length}</span>
        </div>
        <div class="container-list">
          <For each={filteredContainers()}>
            {(container) => (
              <div class="container-row">
                <span class={`container-icon ${container.state === "running" ? "running" : "stopped"}`}>▣</span>
                <span class="container-copy">
                  <strong>{container.name}</strong>
                  <small>
                    {container.image} · {container.status}
                  </small>
                </span>
                <span class={`state ${container.state}`}>{container.state === "running" ? "Running" : "Stopped"}</span>
                <button class="start" disabled={!!fleet.busyContainer} onClick={() => toggleContainer(container)}>
                  {fleet.busyContainer === container.id
                    ? container.state === "running"
                      ? "Stopping…"
                      : "Starting…"
                    : container.state === "running"
                      ? "Stop"
                      : "Start"}
                </button>
              </div>
            )}
          </For>
        </div>
      </section>
    </>
  );
}
function ResourcePage(props: { kind: "images" | "volumes" }) {
  const isImages = props.kind === "images";
  const items = () => (isImages ? filteredImages() : filteredVolumes());
  return (
    <section class="resource-page panel">
      <div class="panel-heading">
        <div>
          <p class="eyebrow">{isImages ? "SHARED INSTALLATION" : `${fleet.selectedHost} / PREFIXED STORAGE`}</p>
          <h2>{isImages ? "Podman images" : "Podman volumes"}</h2>
        </div>
        <span class="resource-count">{items().length}</span>
      </div>
      <div class="resource-list">
        <Show when={items().length} fallback={<div class="resource-empty">No {props.kind} match this search.</div>}>
          <For each={items()}>
            {(item) => (
              <div class="resource-row">
                <span class={`resource-icon ${isImages ? "image" : ""}`}>{isImages ? "◈" : "◫"}</span>
                <span class="resource-copy">
                  <strong>{isImages ? ((item as Image).tags?.[0] ?? "untagged image") : (item as Volume).name}</strong>
                  <small>
                    {isImages
                      ? `${(item as Image).id.slice(0, 19)} · ${formatBytes((item as Image).size)}`
                      : `${(item as Volume).driver} · ${(item as Volume).mountpoint || "Podman managed"}`}
                  </small>
                </span>
              </div>
            )}
          </For>
        </Show>
      </div>
    </section>
  );
}
function Login() {
  const [password, setPassword] = createSignal("");
  return (
    <div class="login-page">
      <div class="login-card">
        <div class="brand">
          <span class="brand-mark">✦</span>
          <span>
            sorry,<strong>portainer</strong>
          </span>
        </div>
        <p class="eyebrow">WELCOME BACK</p>
        <h1>A calmer Podman dashboard.</h1>
        <p class="subtitle">Sign in to see what’s happening across your hosts.</p>
        <form
          onSubmit={async (event) => {
            event.preventDefault();
            await signIn(password());
            setPassword("");
          }}
        >
          <label>
            Admin password
            <input
              type="password"
              value={password()}
              onInput={(event) => setPassword(event.currentTarget.value)}
              disabled={fleet.signingIn}
              placeholder="Enter your password"
            />
          </label>
          <Show when={fleet.loginError}>
            <p class="form-error" role="alert">
              {fleet.loginError}
            </p>
          </Show>
          <button class="primary" type="submit" disabled={fleet.signingIn}>
            {fleet.signingIn ? "Signing in…" : "Open dashboard"} <span>→</span>
          </button>
        </form>
        <small class="login-note">Demo mode opens if the master cannot be reached.</small>
      </div>
    </div>
  );
}
function formatBytes(bytes: number) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

render(
  () => (
    <Router>
      <Route path="*" component={App} />
    </Router>
  ),
  document.getElementById("root")!,
);
