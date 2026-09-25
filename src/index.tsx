import { createEffect, createSignal, For, Show, onMount } from "solid-js";
import { createStore } from "solid-js/store";
import { A, Route, Router, useLocation, useNavigate } from "@solidjs/router";
import { render } from "solid-js/web";
import { hostPath, parseHostPath } from "./host-route";
import { listStacks, type Stack } from "./stack-api";
import { StackEditor, StackList } from "./stacks-ui";
import "./styles.css";

type Host = { host_id: string; prefix: string; hostname: string; engine_version: string };
type Container = { id: string; name: string; image: string; state: string; status: string };
type Volume = { name: string; driver: string; mountpoint: string };
type Image = { id: string; tags?: string[]; size: number; created: number };
type DashboardState = {
  loggedIn: boolean;
  password: string;
  hosts: Host[];
  selectedHost: string;
  containers: Container[];
  volumes: Volume[];
  images: Image[];
  stacks: Stack[];
  stacksLoading: boolean;
  stacksError: string;
  notice: string;
  loading: boolean;
  query: string;
};

const demoHosts: Host[] = [
  { host_id: "local-a", prefix: "local-a-", hostname: "workstation", engine_version: "4.9" },
  { host_id: "local-b", prefix: "local-b-", hostname: "build-node", engine_version: "4.9" },
  { host_id: "local-c", prefix: "local-c-", hostname: "pi-cluster", engine_version: "4.9" },
];
const demoContainers: Record<string, Container[]> = {
  "local-a": [
    { id: "local-a-web-1", name: "local-a-web-1", image: "caddy:latest", state: "running", status: "Up 2 hours" },
    { id: "local-a-db-1", name: "local-a-db-1", image: "postgres:16", state: "running", status: "Up 2 hours" },
  ],
  "local-b": [
    {
      id: "local-b-worker-1",
      name: "local-b-worker-1",
      image: "worker:dev",
      state: "exited",
      status: "Exited (0) 4 minutes ago",
    },
  ],
  "local-c": [],
};

function App() {
  const location = useLocation();
  const [state, setState] = createStore<DashboardState>({
    loggedIn: false,
    password: "",
    hosts: demoHosts,
    selectedHost: "",
    containers: [],
    volumes: [],
    images: [],
    stacks: [],
    stacksLoading: false,
    stacksError: "",
    notice: "Demo cluster · connect the Go API to see live hosts",
    loading: false,
    query: "",
  });
  const [booting, setBooting] = createSignal(true);
  let hostRequest = 0;
  let stackRequest = 0;

  async function loadStacks(hostID: string) {
    const request = ++stackRequest;
    setState({ stacks: [], stacksError: "", stacksLoading: true });
    try {
      const stacks = await listStacks(hostID);
      if (request === stackRequest && state.selectedHost === hostID) setState("stacks", stacks);
    } catch (cause) {
      if (request === stackRequest && state.selectedHost === hostID) {
        setState("stacksError", cause instanceof Error ? cause.message : "Could not load stacks.");
      }
    } finally {
      if (request === stackRequest) setState("stacksLoading", false);
    }
  }

  async function loadHost(host: Host) {
    const request = ++hostRequest;
    ++stackRequest;
    setState({
      selectedHost: host.host_id,
      containers: demoContainers[host.host_id] ?? [],
      volumes: [],
      images: [],
      stacks: [],
      stacksError: "",
      stacksLoading: false,
    });
    void loadStacks(host.host_id);
    const hostID = encodeURIComponent(host.host_id);
    const [info, containers, volumes, images] = await Promise.allSettled([
      fetch(`/api/hosts/${hostID}/info`).then((response) => (response.ok ? (response.json() as Promise<Host>) : null)),
      fetch(`/api/hosts/${hostID}/containers`).then((response) =>
        response.ok ? (response.json() as Promise<Container[]>) : null,
      ),
      fetch(`/api/hosts/${hostID}/volumes`).then((response) =>
        response.ok ? (response.json() as Promise<Volume[]>) : null,
      ),
      fetch(`/api/hosts/${hostID}/images`).then((response) =>
        response.ok ? (response.json() as Promise<Image[]>) : null,
      ),
    ]);
    if (request !== hostRequest) return;
    if (info.status === "fulfilled" && info.value) {
      const hostInfo = info.value;
      setState("hosts", (current) => current.map((item) => (item.host_id === host.host_id ? hostInfo : item)));
    }
    if (containers.status === "fulfilled" && containers.value) setState("containers", containers.value);
    if (volumes.status === "fulfilled" && volumes.value) setState("volumes", volumes.value);
    if (images.status === "fulfilled" && images.value) setState("images", images.value);
  }
  async function login() {
    try {
      const response = await fetch("/api/session/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password: state.password }),
      });
      if (response.ok) {
        const responseHosts = await fetch("/api/hosts");
        if (responseHosts.ok)
          setState({
            loggedIn: true,
            hosts: await responseHosts.json(),
            password: "",
            notice: "Connected to the Podman fleet",
          });
        else setState({ loggedIn: true, password: "", notice: "Connected to the Podman fleet" });
        return;
      }
    } catch {
      /* demo mode */
    }
    setState({ loggedIn: true, password: "", notice: "Welcome back · demo mode is active" });
  }
  function syncHostFromURL() {
    if (!state.loggedIn || booting()) return;
    const route = parseHostPath(location.pathname);
    if (!route) return;
    const host = state.hosts.find((item) => item.host_id === route.hostID);
    if (host && host.host_id !== state.selectedHost) void loadHost(host);
  }
  createEffect(syncHostFromURL);
  onMount(async () => {
    try {
      const response = await fetch("/api/hosts");
      if (response.ok) {
        const hosts = (await response.json()) as Host[];
        setState({ loggedIn: true, hosts, notice: "Connected to the Podman fleet" });
        const route = parseHostPath(location.pathname);
        const initialHost = hosts.find((host) => host.host_id === route?.hostID) ?? hosts[0];
        if (initialHost) await loadHost(initialHost);
      }
    } catch {
      /* demo mode */
    } finally {
      setBooting(false);
    }
  });

  return (
    <Show when={!booting()} fallback={<div class="loading-screen">Opening your Podman fleet…</div>}>
      <Show
        when={state.loggedIn}
        fallback={
          <Login
            password={() => state.password}
            setPassword={(password) => setState("password", password)}
            onLogin={login}
          />
        }
      >
        <Dashboard state={state} setState={setState} refreshStacks={() => loadStacks(state.selectedHost)} />
      </Show>
    </Show>
  );
}

function Dashboard(props: {
  state: DashboardState;
  setState: (key: any, value: any) => void;
  refreshStacks: () => void;
}) {
  const location = useLocation();
  const navigate = useNavigate();
  const route = () => parseHostPath(location.pathname);
  const section = () => route()?.section;
  const query = () => props.state.query.trim().toLowerCase();
  const hosts = () =>
    props.state.hosts.filter(
      (host) => !query() || `${host.host_id} ${host.hostname} ${host.prefix}`.toLowerCase().includes(query()),
    );
  const containers = () =>
    props.state.containers.filter(
      (item) => !query() || `${item.name} ${item.image} ${item.state}`.toLowerCase().includes(query()),
    );
  const volumes = () =>
    props.state.volumes.filter(
      (item) => !query() || `${item.name} ${item.driver} ${item.mountpoint}`.toLowerCase().includes(query()),
    );
  const images = () =>
    props.state.images.filter(
      (item) => !query() || `${item.id} ${(item.tags ?? []).join(" ")}`.toLowerCase().includes(query()),
    );
  const selected = () =>
    route()?.hostID === props.state.selectedHost
      ? props.state.hosts.find((host) => host.host_id === props.state.selectedHost)
      : undefined;
  const selectHost = (host: Host) => {
    navigate(hostPath(host.host_id, "containers"));
  };
  const containerAction = async (container: Container) => {
    const hostID = props.state.selectedHost;
    const operation = container.state === "running" ? "stop" : "start";
    props.setState("loading", true);
    props.setState("notice", `${operation === "start" ? "Starting" : "Stopping"} ${container.name} on ${hostID}…`);
    try {
      const response = await fetch(`/api/hosts/${encodeURIComponent(hostID)}/${operation}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ container_id: container.id }),
      });
      if (!response.ok) throw new Error("container operation failed");
    } catch {
      /* demo mode */
    }
    if (props.state.selectedHost === hostID) {
      props.setState("containers", (items: Container[]) =>
        items.map((item: Container) =>
          item.id === container.id
            ? {
                ...item,
                state: operation === "start" ? "running" : "exited",
                status: operation === "start" ? "Up just now" : "Exited just now",
              }
            : item,
        ),
      );
      props.setState("notice", `${container.name} ${operation === "start" ? "started" : "stopped"}`);
    }
    props.setState("loading", false);
  };
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
        <Show when={route()} fallback={<p class="sidebar-hint">Select a host to browse its resources.</p>}>
          <div class="selected-host">
            <p class="eyebrow">SELECTED HOST</p>
            <strong>{selected()?.host_id ?? route()?.hostID}</strong>
            <small>{selected()?.hostname ?? "Host not found"}</small>
          </div>
          <Show when={selected()}>
            <nav aria-label="Host resources">
              <A href={hostPath(props.state.selectedHost, "containers")} activeClass="active" end>
                Containers <span>{props.state.containers.length}</span>
              </A>
              <A href={hostPath(props.state.selectedHost, "images")} activeClass="active" end>
                Images <span>{props.state.images.length}</span>
              </A>
              <A href={hostPath(props.state.selectedHost, "volumes")} activeClass="active" end>
                Volumes <span>{props.state.volumes.length}</span>
              </A>
              <A href={hostPath(props.state.selectedHost, "stacks")} classList={{ active: section() === "stacks" }}>
                Stacks <span>{props.state.stacks.length}</span>
              </A>
            </nav>
          </Show>
        </Show>
        <div class="sidebar-footer">
          <div class="pulse"></div>All systems nominal
          <button class="text-button" onClick={() => props.setState("loggedIn", false)}>
            Sign out
          </button>
        </div>
      </aside>
      <main>
        <header>
          <div>
            <p class="eyebrow">{section() ? `${route()?.hostID} / PODMAN` : "OVERVIEW / PODMAN FLEET"}</p>
            <h1>
              {section() === "images"
                ? "Images"
                : section() === "volumes"
                  ? "Volumes"
                  : section() === "stacks"
                    ? route()?.action === "new"
                      ? "Create stack"
                      : route()?.action === "edit"
                        ? "Edit stack"
                        : "Stacks"
                    : section() === "containers"
                      ? "Containers"
                      : "Hosts"}{" "}
              <span>✦</span>
            </h1>
            <p class="subtitle">
              {section() === "images"
                ? "Images available on this host's Podman installation."
                : section() === "volumes"
                  ? `Persistent storage owned by ${route()?.hostID}.`
                  : section() === "stacks"
                    ? `Compose projects saved on ${route()?.hostID}.`
                    : section() === "containers"
                      ? `Containers on ${route()?.hostID}.`
                      : "Choose a host to browse its resources."}
            </p>
          </div>
          <div class="header-actions">
            <label class="search-box">
              <span>⌕</span>
              <input
                value={props.state.query}
                onInput={(event) => props.setState("query", event.currentTarget.value)}
                placeholder={section() ? `Search ${section()}` : "Search hosts"}
                aria-label="Search"
              />
              <kbd>⌘ K</kbd>
            </label>
            <div class="avatar">B</div>
          </div>
        </header>
        <div class="notice">{props.state.notice}</div>
        <Show when={!route()}>
          <Overview hosts={hosts()} selectHost={selectHost} />
        </Show>
        <Show when={route() && !selected()}>
          <div class="resource-empty">
            {props.state.hosts.some((host) => host.host_id === route()?.hostID)
              ? "Loading host…"
              : "Host not found. Choose a host from the overview."}
          </div>
        </Show>
        <Show when={selected()}>
          <Show when={section() === "containers"}>
            <HostPage host={selected()} containers={containers()} onContainerAction={containerAction} />
          </Show>
          <Show when={section() === "images"}>
            <ResourcePage kind="images" selectedHost={props.state.selectedHost} images={images()} volumes={[]} />
          </Show>
          <Show when={section() === "volumes"}>
            <ResourcePage kind="volumes" selectedHost={props.state.selectedHost} images={[]} volumes={volumes()} />
          </Show>
          <Show when={section() === "stacks"}>
            <Show
              when={route()?.action}
              fallback={
                <StackList
                  hostID={props.state.selectedHost}
                  stacks={props.state.stacks}
                  loading={props.state.stacksLoading}
                  error={props.state.stacksError}
                  query={query()}
                  refresh={props.refreshStacks}
                />
              }
            >
              <StackEditor
                hostID={props.state.selectedHost}
                stackName={route()?.stackName}
                onSaved={props.refreshStacks}
              />
            </Show>
          </Show>
        </Show>
      </main>
    </div>
  );
}
function Overview(props: {
  hosts: Host[];
  selectHost: (host: Host) => void;
}) {
  return (
    <>
      <section class="panel">
        <div class="panel-heading">
          <div>
            <p class="eyebrow">YOUR FLEET</p>
            <h2>Podman hosts</h2>
          </div>
        </div>
        <div class="host-list">
          <For each={props.hosts}>
            {(host) => (
              <button class="host-row" onClick={() => props.selectHost(host)}>
                <span class="host-icon">⌂</span>
                <span class="host-copy">
                  <strong>{host.host_id}</strong>
                  <small>
                    {host.hostname} · Podman {host.engine_version}
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
function HostPage(props: {
  host?: Host;
  containers: Container[];
  onContainerAction: (container: Container) => Promise<void>;
}) {
  return (
    <>
      <Show when={props.host}>
        <section class="host-info-grid host-info-card">
          <div>
            <span>HOSTNAME</span>
            <strong>{props.host?.hostname}</strong>
          </div>
          <div>
            <span>PODMAN</span>
            <strong>{props.host?.engine_version}</strong>
          </div>
          <div>
            <span>CONTAINER PREFIX</span>
            <strong>{props.host?.prefix}</strong>
          </div>
        </section>
      </Show>
      <section class="panel resource-preview">
        <div class="panel-heading">
          <h2>Containers</h2>
          <span class="resource-count">{props.containers.length}</span>
        </div>
        <div class="container-list">
          <For each={props.containers}>
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
                <button class="start" onClick={() => props.onContainerAction(container)}>
                  {container.state === "running" ? "Stop" : "Start"}
                </button>
              </div>
            )}
          </For>
        </div>
      </section>
    </>
  );
}
function ResourcePage(props: { kind: "images" | "volumes"; selectedHost: string; images: Image[]; volumes: Volume[] }) {
  const isImages = props.kind === "images";
  return (
    <section class="resource-page panel">
      <div class="panel-heading">
        <div>
          <p class="eyebrow">{isImages ? "SHARED INSTALLATION" : `${props.selectedHost} / PREFIXED STORAGE`}</p>
          <h2>{isImages ? "Podman images" : "Podman volumes"}</h2>
        </div>
        <span class="resource-count">{isImages ? props.images.length : props.volumes.length}</span>
      </div>
      <div class="resource-list">
        <Show
          when={isImages ? props.images.length : props.volumes.length}
          fallback={<div class="resource-empty">No {props.kind} match this search.</div>}
        >
          <For each={isImages ? props.images : props.volumes}>
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
function Login(props: { password: () => string; setPassword: (v: string) => void; onLogin: () => void }) {
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
          onSubmit={(event) => {
            event.preventDefault();
            props.onLogin();
          }}
        >
          <label>
            Admin password
            <input
              type="password"
              value={props.password()}
              onInput={(event) => props.setPassword(event.currentTarget.value)}
              placeholder="Enter your password"
            />
          </label>
          <button class="primary" type="submit">
            Open dashboard <span>→</span>
          </button>
        </form>
        <small class="login-note">Demo mode is available while the API is offline.</small>
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
