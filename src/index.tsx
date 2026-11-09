import { createEffect, createSignal, For, Match, Show, Switch, onMount, untrack } from "solid-js";
import { A, Route, Router, useLocation, useNavigate } from "@solidjs/router";
import { render } from "solid-js/web";
import {
  boot,
  filteredContainers,
  filteredHosts,
  fleet,
  selectedHost,
  setQuery,
  showHost,
  signIn,
  toggleContainer,
} from "./fleet-store";
import { EnrollmentPage } from "./enrollment-ui";
import { HealthNote } from "./health-note";
import { enrollmentPath, hostPath, newStackPath, parseHostPath } from "./host-route";
import {
  ArrowRightIcon,
  BrandMark,
  ContainerIcon,
  EyeIcon,
  EyeSlashIcon,
  PlusIcon,
  SearchIcon,
  ServerIcon,
} from "./icons";
import { CreateStoreCard, HostSecretWarnings } from "./secrets-ui";
import { Sidebar } from "./sidebar";
import { StackDetail, StackEditor, StackList } from "./stacks-ui";
import { VolumeDetailPage, VolumeList } from "./volumes-ui";
import { ImageDetailPage, ImageList } from "./images-ui";
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
      <Sidebar />
      <main>
        <header>
          <div>
            <Show when={heading().hostList && route()} fallback={<p class="eyebrow">{heading().eyebrow}</p>}>
              {(current) => (
                <nav class="breadcrumb" aria-label="Breadcrumb">
                  <A href="/">Hosts</A>
                  <span aria-hidden="true">/</span>
                  <span>{current().hostID}</span>
                </nav>
              )}
            </Show>
            <h1>{heading().title}</h1>
            <Show when={heading().hostList && selected()} fallback={<p class="subtitle">{heading().subtitle}</p>}>
              {(host) => (
                <dl class="host-chips">
                  <div>
                    <dt>Hostname</dt>
                    <dd>{host().hostname}</dd>
                  </div>
                  <div>
                    <dt>Podman</dt>
                    <dd>{host().engine_version}</dd>
                  </div>
                  <div>
                    <dt>Prefix</dt>
                    <dd class="mono">{host().prefix}</dd>
                  </div>
                </dl>
              )}
            </Show>
          </div>
          <div class="header-actions">
            <Show when={!route() && !enrolling()}>
              <A class="add-button solid" href={enrollmentPath}>
                <PlusIcon />
                Enroll host
              </A>
            </Show>
            <Show when={section() === "stacks" && !route()?.action && selected()}>
              <A class="add-button solid" href={newStackPath(fleet.selectedHost)}>
                <PlusIcon />
                New stack
              </A>
            </Show>
            <label class="search-box">
              <SearchIcon />
              <input
                type="search"
                value={fleet.query}
                onInput={(event) => setQuery(event.currentTarget.value)}
                placeholder={heading().search}
                aria-label={heading().search}
              />
              <kbd>⌘K</kbd>
            </label>
          </div>
        </header>
        <Show when={fleet.notice}>
          <div class="notice" role="status">
            {fleet.notice}
          </div>
        </Show>
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
            <Switch fallback={<ImageList />}>
              <Match when={route()?.action === "view"}>
                <ImageDetailPage />
              </Match>
            </Switch>
          </Show>
          <Show when={section() === "volumes"}>
            <Switch fallback={<VolumeList />}>
              <Match when={route()?.action === "view"}>
                <VolumeDetailPage />
              </Match>
            </Switch>
          </Show>
          <Show when={section() === "stacks"}>
            <Switch fallback={<StackList />}>
              <Match when={route()?.action === "view"}>
                <StackDetail />
              </Match>
              <Match when={route()?.action}>
                <StackEditor />
              </Match>
            </Switch>
          </Show>
        </Show>
      </main>
    </div>
  );
}
/**
 * The header text for the page at pathname. A host's list pages (hostList)
 * show a breadcrumb back to the hosts and the host's chips instead of the
 * eyebrow and subtitle.
 */
function pageHeading(pathname: string): {
  eyebrow: string;
  title: string;
  subtitle: string;
  search: string;
  hostList?: boolean;
} {
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
  const hostList = !route.action;
  switch (route.section) {
    case "images":
      return route.action === "view"
        ? { eyebrow, search, title: "Image", subtitle: `Image on ${route.hostID}'s Podman installation.` }
        : {
            eyebrow,
            search,
            hostList,
            title: "Images",
            subtitle: "Images available on this host's Podman installation.",
          };
    case "volumes":
      return route.action === "view"
        ? { eyebrow, search, title: "Volume", subtitle: `Persistent storage on ${route.hostID}.` }
        : { eyebrow, search, hostList, title: "Volumes", subtitle: `Persistent storage owned by ${route.hostID}.` };
    case "containers":
      return { eyebrow, search, hostList, title: "Containers", subtitle: `Containers on ${route.hostID}.` };
    case "stacks":
      return {
        eyebrow,
        search,
        hostList,
        title:
          route.action === "new"
            ? "Create stack"
            : route.action === "edit"
              ? "Edit stack"
              : route.action === "view"
                ? "Stack"
                : "Stacks",
        subtitle: `Compose projects saved on ${route.hostID}.`,
      };
  }
}
function Overview() {
  const navigate = useNavigate();
  return (
    <>
      <CreateStoreCard />
      <section class="fleet-section" aria-labelledby="fleet-heading">
        <div class="section-heading">
          <h2 id="fleet-heading">Podman hosts</h2>
          <p>
            {filteredHosts().length} {filteredHosts().length === 1 ? "host" : "hosts"}
          </p>
        </div>
        <div class="host-grid">
          <For each={filteredHosts()}>
            {(host) => (
              <button class="host-card" onClick={() => navigate(hostPath(host.host_id, "containers"))}>
                <div class="host-card-top">
                  <div class="host-name">
                    <span class="host-icon" aria-hidden="true">
                      <ServerIcon size={22} />
                    </span>
                    <div>
                      <strong>{host.host_id}</strong>
                      <small>{host.hostname}</small>
                    </div>
                  </div>
                  <span class="online">
                    <i class="dot green"></i>Online
                  </span>
                </div>
                <dl class="host-meta">
                  <div>
                    <dt>Podman</dt>
                    <dd>{host.engine_version}</dd>
                  </div>
                  <div>
                    <dt>Prefix</dt>
                    <dd class="mono">{host.prefix}</dd>
                  </div>
                </dl>
                <div class="host-foot">
                  <span>Browse resources</span>
                  {host.swap_active ? <span class="warn-chip">Swap active</span> : <ArrowRightIcon />}
                </div>
              </button>
            )}
          </For>
          <A class="enroll-tile" href={enrollmentPath}>
            <span class="host-icon" aria-hidden="true">
              <PlusIcon size={20} />
            </span>
            <strong>Enroll a host</strong>
            <small>Create a one-time token, then run the agent's enroll command on the new machine.</small>
          </A>
        </div>
      </section>
    </>
  );
}
function HostPage() {
  return (
    <>
      <Show when={selectedHost()}>{(host) => <HostSecretWarnings host={host()} />}</Show>
      <section class="panel resource-preview" aria-labelledby="containers-heading">
        <div class="panel-heading">
          <div>
            <h2 id="containers-heading">All containers</h2>
          </div>
          <span class="resource-count">{filteredContainers().length}</span>
        </div>
        <div class="data-scroll">
          <div class="data-table cols-containers">
            <div class="data-head">
              <span>Name</span>
              <span>Image</span>
              <span>State</span>
              <span>Uptime</span>
              <span class="right">Action</span>
            </div>
            <For
              each={filteredContainers()}
              fallback={<div class="resource-empty">No containers match this search.</div>}
            >
              {(container) => {
                const running = () => container.state === "running";
                const busy = () => fleet.busyContainer === container.id;
                return (
                  <div class="data-row">
                    <div class="cell-name">
                      <span class="resource-icon" aria-hidden="true">
                        <ContainerIcon size={17} />
                      </span>
                      <div>
                        <strong>{container.name}</strong>
                      </div>
                    </div>
                    <span class="chip-code">{container.image}</span>
                    <span class="state-line">
                      <span class={`state ${running() ? "running" : "stopped"}`}>
                        <i class={`dot ${running() ? "green" : "grey"}`}></i>
                        {running() ? "Running" : "Stopped"}
                      </span>
                      <HealthNote facts={container} />
                    </span>
                    <span class="cell-value">{running() ? container.status : "—"}</span>
                    <div class="cell-actions">
                      <button
                        class={running() ? "stop" : "start"}
                        disabled={!!fleet.busyContainer}
                        onClick={() => toggleContainer(container)}
                      >
                        {busy() ? (running() ? "Stopping…" : "Starting…") : running() ? "Stop" : "Start"}
                      </button>
                    </div>
                  </div>
                );
              }}
            </For>
          </div>
        </div>
      </section>
    </>
  );
}
function Login() {
  const [password, setPassword] = createSignal("");
  const [showPassword, setShowPassword] = createSignal(false);
  // The design's status grid: 96 dots, a few lit, one blinking.
  const dots = Array.from({ length: 96 }, (_, i) => {
    const r = (i * 37 + (i % 7) * 11) % 23;
    if (r === 7) return "amber blink";
    if (r === 3 || r === 11 || r === 17) return "lit";
    if (r === 19) return "faint";
    return "";
  });
  return (
    <div class="login-page">
      <section class="login-hero" aria-hidden="true">
        <div class="brand">
          <BrandMark />
          <span>sorry,portainer</span>
        </div>
        <div class="login-copy">
          <span class="eyebrow">Welcome back</span>
          <p class="login-headline">A calmer Podman dashboard.</p>
          <p class="login-lede">Sign in to see what’s happening across your hosts.</p>
        </div>
        <div class="dot-grid">
          <For each={dots}>{(dot) => <span class={dot}></span>}</For>
        </div>
      </section>
      <div class="login-side">
        <div class="login-form">
          <div>
            <h1>Sign in</h1>
            <p class="subtitle">Use the admin password set when the master was installed.</p>
          </div>
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              await signIn(password());
              setPassword("");
            }}
          >
            <label for="admin-password">
              Admin password
              <span class={`field ${fleet.loginError ? "error" : ""}`}>
                <input
                  id="admin-password"
                  type={showPassword() ? "text" : "password"}
                  autocomplete="current-password"
                  value={password()}
                  onInput={(event) => setPassword(event.currentTarget.value)}
                  disabled={fleet.signingIn}
                  placeholder="Enter your password"
                  aria-describedby="login-error"
                />
                <button
                  type="button"
                  onClick={() => setShowPassword(!showPassword())}
                  aria-label={showPassword() ? "Hide password" : "Show password"}
                >
                  <Show when={showPassword()} fallback={<EyeIcon />}>
                    <EyeSlashIcon />
                  </Show>
                </button>
              </span>
            </label>
            <Show when={fleet.loginError}>
              <p class="form-error" id="login-error" role="alert">
                {fleet.loginError}
              </p>
            </Show>
            <button class="primary" type="submit" disabled={fleet.signingIn}>
              {fleet.signingIn ? "Signing in…" : "Open dashboard"} <ArrowRightIcon size={16} />
            </button>
          </form>
          <small class="login-note">Demo mode opens if the master cannot be reached.</small>
        </div>
      </div>
    </div>
  );
}

render(
  () => (
    <Router>
      <Route path="*" component={App} />
    </Router>
  ),
  document.getElementById("root")!,
);
