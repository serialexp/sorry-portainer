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
  signOut,
  toggleContainer,
} from "./fleet-store";
import { EnrollmentPage } from "./enrollment-ui";
import { HealthNote } from "./health-note";
import { enrollmentPath, hostPath, parseHostPath } from "./host-route";
import { HostSecretWarnings, SecretStoreBanner } from "./secrets-ui";
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
          <button class="text-button" onClick={() => void signOut()}>
            Sign out
          </button>
        </div>
      </aside>
      <main>
        <header>
          <div>
            <p class="eyebrow">{heading().eyebrow}</p>
            <h1>{heading().title}</h1>
            <p class="subtitle">{heading().subtitle}</p>
          </div>
          <div class="header-actions">
            <Show when={!route() && !enrolling()}>
              <A class="add-button solid" href={enrollmentPath}>
                Enroll host
              </A>
            </Show>
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
      return route.action === "view"
        ? { eyebrow, search, title: "Image", subtitle: `Image on ${route.hostID}'s Podman installation.` }
        : { eyebrow, search, title: "Images", subtitle: "Images available on this host's Podman installation." };
    case "volumes":
      return route.action === "view"
        ? { eyebrow, search, title: "Volume", subtitle: `Persistent storage on ${route.hostID}.` }
        : { eyebrow, search, title: "Volumes", subtitle: `Persistent storage owned by ${route.hostID}.` };
    case "containers":
      return { eyebrow, search, title: "Containers", subtitle: `Containers on ${route.hostID}.` };
    case "stacks":
      return {
        eyebrow,
        search,
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
                    ⌂
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
                {host.swap_active ? <span class="warn-chip">Swap active</span> : <span aria-hidden="true">→</span>}
              </div>
            </button>
          )}
        </For>
        <A class="enroll-tile" href={enrollmentPath}>
          <span class="host-icon" aria-hidden="true">
            +
          </span>
          <strong>Enroll a host</strong>
          <small>Create a one-time token, then run the agent's enroll command on the new machine.</small>
        </A>
      </div>
    </section>
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
                        ▣
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
          <span class="brand-mark">✦</span>
          <span>
            sorry,<strong>portainer</strong>
          </span>
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
                  <svg
                    width="18"
                    height="18"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    aria-hidden="true"
                  >
                    <path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"></path>
                    <circle cx="12" cy="12" r="3"></circle>
                  </svg>
                </button>
              </span>
            </label>
            <Show when={fleet.loginError}>
              <p class="form-error" id="login-error" role="alert">
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
