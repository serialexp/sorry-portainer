import { createStore } from "solid-js/store";
import { fleet, sessionEnded } from "./fleet-store";
import { getStackImages, type ServiceImage, updateService } from "./image-updates";
import { StackOperationError } from "./stack-api";

// Image checks and updates for the stack page being shown. The stack page's
// service rows read it directly, like the fleet store.

type ImageUpdateState = {
  host: string;
  stack: string;
  services: ServiceImage[];
  /** True until the first check of this stack answers. */
  loading: boolean;
  /** A forced check (Check now) is running. */
  checking: boolean;
  error: string;
  /** The service whose confirm row is open. */
  confirm: string;
  /** The service being updated. */
  updating: string;
  /** The last update's result, shown with the stack's other operation results. */
  result: { failed: boolean; title: string; output: string } | null;
  /** Counts finished updates, so the stack page reloads after each. */
  completed: number;
  /** Unix seconds, ticked while a stack page is open, for "checked X ago". */
  now: number;
};

const [state, setState] = createStore<ImageUpdateState>({
  host: "",
  stack: "",
  services: [],
  loading: false,
  checking: false,
  error: "",
  confirm: "",
  updating: "",
  result: null,
  completed: 0,
  now: Math.floor(Date.now() / 1000),
});

export const imageUpdates = state;

let request = 0;

const current = (host: string, stack: string) => state.host === host && state.stack === stack;

/** Starts over for another stack page: nothing checked, nothing open. */
export function showImagesOf(host: string, stack: string) {
  ++request;
  setState({
    host,
    stack,
    services: [],
    loading: false,
    checking: false,
    error: "",
    confirm: "",
    updating: "",
    result: null,
  });
}

/**
 * Checks the shown stack's images. A page open uses the agent's cached
 * answers; Check now (force) asks the registry again. While busy is set the
 * rows say "Checking…" instead of showing answers that are known to be stale.
 */
export async function checkImages(force = false, busy = force) {
  const { host, stack } = state;
  if (!host || !stack || fleet.demo) return;
  const id = ++request;
  setState({ error: "", loading: !state.services.length, checking: busy });
  try {
    const images = await getStackImages(host, stack, force);
    if (id !== request || !current(host, stack)) return;
    setState({ services: images.services ?? [] });
  } catch (cause) {
    if (id === request && current(host, stack) && !sessionEnded(cause))
      setState({ error: cause instanceof Error ? cause.message : "Could not check images." });
  } finally {
    if (id === request) setState({ loading: false, checking: false });
  }
  tick();
}

export function serviceImage(service: string): ServiceImage | undefined {
  return state.services.find((img) => img.service === service);
}

export function askUpdate(service: string) {
  setState({ confirm: service });
}

export function cancelUpdate() {
  setState({ confirm: "" });
}

export function hideUpdateResult() {
  setState({ result: null });
}

export function tick() {
  setState({ now: Math.floor(Date.now() / 1000) });
}

/** Pulls the service's image and, when the stack runs, recreates it; then checks again. */
export async function confirmUpdate(service: string, stackRuns: boolean) {
  const { host, stack } = state;
  setState({ confirm: "", updating: service, result: null });
  try {
    const done = await updateService(host, stack, service);
    if (!current(host, stack)) return;
    const title = stackRuns ? `Updated ${service}` : `Pulled the newest image for ${service}`;
    setState({ result: { failed: false, title, output: done.output ?? "" } });
  } catch (cause) {
    if (current(host, stack) && !sessionEnded(cause))
      setState({
        result: {
          failed: true,
          title: cause instanceof Error ? cause.message : `Could not update ${service}.`,
          output: cause instanceof StackOperationError ? cause.output : "",
        },
      });
  } finally {
    if (current(host, stack)) {
      setState((s) => ({ updating: "", completed: s.completed + 1 }));
      // The agent forgot its cached answer for the image, so this asks the
      // registry for that image only; until it answers, the old answer is stale.
      void checkImages(false, true);
    }
  }
}
