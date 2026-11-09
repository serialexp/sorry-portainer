import { createStore } from "solid-js/store";
import {
  type Container,
  containerOperation,
  deleteImage,
  deleteVolume,
  type Host,
  hostInfo,
  type Image,
  listContainers,
  listHosts,
  listImages,
  listVolumes,
  login,
  logout,
  MasterUnreachableError,
  UnauthorizedError,
  type Volume,
} from "./fleet-api";
import { listStacks, type Stack } from "./stack-api";

// Dashboard state: the session, the fleet's hosts and the selected host's
// resources. Components read it directly instead of receiving it through
// props, like the secrets store.

type FleetState = {
  /** "booting" until the first host list answers or fails. */
  phase: "booting" | "login" | "ready";
  /** Set when sign-in could not reach the master; data is made up. */
  demo: boolean;
  signingIn: boolean;
  loginError: string;
  hosts: Host[];
  selectedHost: string;
  containers: Container[];
  volumes: Volume[];
  images: Image[];
  stacks: Stack[];
  stacksLoading: boolean;
  stacksError: string;
  /** ID of the container being started or stopped. */
  busyContainer: string;
  notice: string;
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

const [fleet, setFleet] = createStore<FleetState>({
  phase: "booting",
  demo: false,
  signingIn: false,
  loginError: "",
  hosts: [],
  selectedHost: "",
  containers: [],
  volumes: [],
  images: [],
  stacks: [],
  stacksLoading: false,
  stacksError: "",
  busyContainer: "",
  notice: "",
  query: "",
});

export { fleet };

// Answers for a host that is no longer selected, or from an older request
// for the same host, are dropped.
let hostRequest = 0;
let stackRequest = 0;
let hostsRequest = 0;

// Other stores' resets, run whenever the dashboard returns to the login form
// so nothing from one session is shown in the next.
const sessionResets: (() => void)[] = [];

/** Registers reset to run when the session ends or the admin signs out. */
export function onSessionEnd(reset: () => void) {
  sessionResets.push(reset);
}

function message(cause: unknown, fallback: string): string {
  return cause instanceof Error ? cause.message : fallback;
}

/**
 * Sends the dashboard back to the login form when cause says the session
 * has ended, and reports whether it did.
 */
export function sessionEnded(cause: unknown): boolean {
  if (!(cause instanceof UnauthorizedError) || fleet.demo) return false;
  showLogin("Your session has ended. Sign in again.");
  return true;
}

function clearHost() {
  ++hostRequest;
  ++stackRequest;
  setFleet({
    selectedHost: "",
    containers: [],
    volumes: [],
    images: [],
    stacks: [],
    stacksLoading: false,
    stacksError: "",
    busyContainer: "",
  });
}

/** Opens the dashboard if the browser already has a session. */
export async function boot() {
  try {
    setFleet({ hosts: await listHosts(), phase: "ready", notice: "" });
  } catch {
    setFleet("phase", "login");
  }
}

export async function signIn(password: string) {
  if (fleet.signingIn) return;
  setFleet({ signingIn: true, loginError: "" });
  try {
    await login(password);
    setFleet({ hosts: await listHosts(), demo: false, phase: "ready", notice: "" });
  } catch (cause) {
    if (cause instanceof MasterUnreachableError)
      setFleet({
        demo: true,
        hosts: demoHosts,
        phase: "ready",
        notice: "Demo cluster · the master could not be reached, so nothing here is real",
      });
    else setFleet("loginError", message(cause, "Could not sign in."));
  } finally {
    setFleet("signingIn", false);
  }
}

function showLogin(loginError: string) {
  clearHost();
  ++hostsRequest;
  setFleet({ phase: "login", demo: false, hosts: [], loginError, notice: "", query: "" });
  for (const reset of sessionResets) reset();
}

/** Ends the session on the master, then returns to the login form. */
export async function signOut() {
  if (fleet.demo) {
    showLogin("");
    return;
  }
  try {
    await logout();
    showLogin("");
  } catch (cause) {
    // The session may still be valid; say so rather than pretend.
    setFleet("notice", message(cause, "Could not sign out."));
  }
}

/** Reloads the host list, for example after a host was enrolled or removed. */
export async function loadHosts() {
  if (fleet.demo) return;
  const request = ++hostsRequest;
  try {
    const hosts = await listHosts();
    if (request === hostsRequest) setFleet("hosts", hosts);
  } catch (cause) {
    if (request === hostsRequest && !sessionEnded(cause)) setFleet("notice", message(cause, "Could not load hosts."));
  }
}

export async function loadStacks() {
  const hostID = fleet.selectedHost;
  if (!hostID) return;
  const request = ++stackRequest;
  // The list stays on screen while it reloads; changing host clears it (clearHost).
  setFleet({ stacksError: "", stacksLoading: !fleet.demo });
  if (fleet.demo) return;
  try {
    const stacks = await listStacks(hostID);
    if (request === stackRequest) setFleet("stacks", stacks);
  } catch (cause) {
    if (request === stackRequest && !sessionEnded(cause))
      setFleet("stacksError", message(cause, "Could not load stacks."));
  } finally {
    if (request === stackRequest) setFleet("stacksLoading", false);
  }
}

async function loadContainers(hostID: string, request: number) {
  const containers = await listContainers(hostID);
  if (request === hostRequest) setFleet("containers", containers);
}

/**
 * Selects the host the URL names and loads its resources. Does nothing when
 * it is already selected; an unknown host clears the selection.
 */
export async function showHost(hostID: string) {
  if (hostID === fleet.selectedHost) return;
  clearHost();
  if (!fleet.hosts.some((host) => host.host_id === hostID)) return;
  const request = hostRequest;
  setFleet("selectedHost", hostID);
  void loadStacks();
  if (fleet.demo) {
    setFleet("containers", demoContainers[hostID] ?? []);
    return;
  }
  const loads = await Promise.allSettled([
    hostInfo(hostID).then((info) => {
      if (request === hostRequest) setFleet("hosts", (host) => host.host_id === hostID, info);
    }),
    loadContainers(hostID, request),
    listVolumes(hostID).then((volumes) => request === hostRequest && setFleet("volumes", volumes)),
    listImages(hostID).then((images) => request === hostRequest && setFleet("images", images)),
  ]);
  if (request !== hostRequest) return;
  const failures = loads.flatMap((load) => (load.status === "rejected" ? [load.reason] : []));
  if (failures.some(sessionEnded)) return;
  if (failures.length) setFleet("notice", `${hostID}: ${message(failures[0], "Could not load the host.")}`);
}

/** Starts a stopped container or stops a running one, then reloads the list. */
export async function toggleContainer(container: Container) {
  const hostID = fleet.selectedHost;
  if (!hostID || fleet.busyContainer) return;
  const request = hostRequest;
  const operation = container.state === "running" ? "stop" : "start";
  setFleet({
    busyContainer: container.id,
    notice: `${operation === "start" ? "Starting" : "Stopping"} ${container.name} on ${hostID}…`,
  });
  try {
    if (fleet.demo) {
      setFleet("containers", (item) => item.id === container.id, {
        state: operation === "start" ? "running" : "exited",
        status: operation === "start" ? "Up just now" : "Exited just now",
      });
    } else {
      await containerOperation(hostID, container.id, operation);
      await loadContainers(hostID, request);
    }
    if (request === hostRequest)
      setFleet("notice", `${container.name} ${operation === "start" ? "started" : "stopped"}`);
  } catch (cause) {
    if (request === hostRequest && !sessionEnded(cause))
      setFleet("notice", message(cause, `Could not ${operation} ${container.name}.`));
  } finally {
    if (request === hostRequest) setFleet("busyContainer", "");
  }
}

/**
 * Removes a volume of the selected host and drops it from the list. The
 * failure is thrown so the page can show the host's reason, such as a volume
 * that a container still mounts. Nothing changes if the host is no longer
 * selected when the answer arrives.
 */
export async function removeVolume(name: string) {
  const hostID = fleet.selectedHost;
  if (!hostID) return;
  if (!fleet.demo) await deleteVolume(hostID, name);
  if (hostID === fleet.selectedHost) setFleet("volumes", (list) => list.filter((volume) => volume.name !== name));
}

/** Removes an image of the selected host and drops it from the list; see removeVolume. */
export async function removeImage(id: string) {
  const hostID = fleet.selectedHost;
  if (!hostID) return;
  if (!fleet.demo) await deleteImage(hostID, id);
  if (hostID === fleet.selectedHost) setFleet("images", (list) => list.filter((image) => image.id !== id));
}

export function setQuery(query: string) {
  setFleet("query", query);
}

const search = () => fleet.query.trim().toLowerCase();
const matches = (text: string) => !search() || text.toLowerCase().includes(search());

export const searchTerm = search;
export const filteredHosts = () =>
  fleet.hosts.filter((host) => matches(`${host.host_id} ${host.hostname} ${host.prefix}`));
export const filteredContainers = () =>
  fleet.containers.filter((item) => matches(`${item.name} ${item.image} ${item.state}`));
export const filteredVolumes = () =>
  fleet.volumes.filter((item) => matches(`${item.name} ${item.driver} ${item.mountpoint}`));
export const filteredImages = () => fleet.images.filter((item) => matches(`${item.id} ${(item.tags ?? []).join(" ")}`));
export const filteredStacks = () => fleet.stacks.filter((stack) => matches(`${stack.name} ${stack.project}`));
export const selectedHost = () => fleet.hosts.find((host) => host.host_id === fleet.selectedHost);
