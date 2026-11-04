import { createStore, reconcile } from "solid-js/store";
import {
  type Delivery,
  deleteStackSecret,
  getStoreState,
  initializeStore,
  listStackSecrets,
  lockStore,
  putStackSecret,
  type SecretEntry,
  SecretsUnavailableError,
  type StoreState,
  unlockStore,
} from "./secrets-api";
import { onSessionEnd, sessionEnded } from "./fleet-store";

// Secret-store state shared by the store banner and the per-stack panels.
// Components read it directly instead of receiving it through props.

export type StackSecretsState = {
  entries: SecretEntry[];
  /** Secret names the stack's Compose file uses (from the stack inspect). */
  referenced: string[];
  delivery: Delivery | null;
  loading: boolean;
  busy: string;
  error: string;
};

type SecretsState = {
  /** "unavailable" when the master has no secret store configured. */
  store: StoreState | "unknown" | "unavailable";
  storeBusy: boolean;
  storeError: string;
  stacks: Record<string, StackSecretsState>;
};

const initial = (): SecretsState => ({
  store: "unknown",
  storeBusy: false,
  storeError: "",
  stacks: {},
});

const [secrets, setSecrets] = createStore<SecretsState>(initial());

export { secrets };

// Bumped when the session ends, so answers to earlier requests are dropped.
let generation = 0;

onSessionEnd(() => {
  ++generation;
  // Replace stacks wholesale; a plain set would merge the old entries in.
  setSecrets(reconcile(initial()));
});

export function stackKey(hostID: string, stack: string): string {
  return `${hostID}/${stack}`;
}

const emptyStack = (): StackSecretsState => ({
  entries: [],
  referenced: [],
  delivery: null,
  loading: false,
  busy: "",
  error: "",
});

export function stackSecrets(hostID: string, stack: string): StackSecretsState {
  return secrets.stacks[stackKey(hostID, stack)] ?? emptyStack();
}

function ensureStack(key: string) {
  if (!secrets.stacks[key]) setSecrets("stacks", key, emptyStack());
}

function message(cause: unknown, fallback: string): string {
  return cause instanceof Error ? cause.message : fallback;
}

export async function refreshStoreState() {
  const started = generation;
  try {
    const store = await getStoreState();
    if (started === generation) setSecrets({ store, storeError: "" });
  } catch (cause) {
    if (started !== generation || sessionEnded(cause)) return;
    if (cause instanceof SecretsUnavailableError) setSecrets({ store: "unavailable", storeError: "" });
    else setSecrets("storeError", message(cause, "Could not read the secret store state."));
  }
}

async function storeAction(action: () => Promise<StoreState>, fallback: string): Promise<boolean> {
  if (secrets.storeBusy) return false;
  const started = generation;
  setSecrets({ storeBusy: true, storeError: "" });
  try {
    const state = await action();
    if (started !== generation) return false;
    setSecrets("store", state);
    return true;
  } catch (cause) {
    if (started === generation && !sessionEnded(cause)) setSecrets("storeError", message(cause, fallback));
    return false;
  } finally {
    if (started === generation) setSecrets("storeBusy", false);
  }
}

export function initializeSecretStore(passphrase: string) {
  return storeAction(() => initializeStore(passphrase), "Could not create the secret store.");
}

export function unlockSecretStore(passphrase: string) {
  return storeAction(() => unlockStore(passphrase), "Could not unlock the secret store.");
}

export function lockSecretStore() {
  return storeAction(() => lockStore(), "Could not lock the secret store.");
}

export function setReferencedSecrets(hostID: string, stack: string, names: string[]) {
  const key = stackKey(hostID, stack);
  ensureStack(key);
  setSecrets("stacks", key, "referenced", [...names].sort());
}

export async function loadStackSecrets(hostID: string, stack: string) {
  const key = stackKey(hostID, stack);
  const started = generation;
  ensureStack(key);
  setSecrets("stacks", key, { loading: true, error: "" });
  try {
    const result = await listStackSecrets(hostID, stack);
    if (started !== generation) return;
    setSecrets("store", result.state);
    setSecrets("stacks", key, { entries: result.secrets ?? [], delivery: result.delivery });
  } catch (cause) {
    if (started !== generation || sessionEnded(cause)) return;
    if (cause instanceof SecretsUnavailableError) setSecrets("store", "unavailable");
    setSecrets("stacks", key, "error", message(cause, "Could not load stack secrets."));
  } finally {
    if (started === generation) setSecrets("stacks", key, "loading", false);
  }
}

async function stackAction(hostID: string, stack: string, name: string, action: () => Promise<Delivery>) {
  const key = stackKey(hostID, stack);
  ensureStack(key);
  if (secrets.stacks[key].busy) return false;
  const started = generation;
  setSecrets("stacks", key, { busy: name, error: "" });
  try {
    const delivery = await action();
    if (started !== generation) return false;
    setSecrets("stacks", key, "delivery", delivery);
    return true;
  } catch (cause) {
    if (started === generation && !sessionEnded(cause))
      setSecrets("stacks", key, "error", message(cause, "The secret change failed."));
    return false;
  } finally {
    if (started === generation) {
      setSecrets("stacks", key, "busy", "");
      // Refresh names and the store state (a 423 means it was locked meanwhile).
      await loadStackSecrets(hostID, stack);
    }
  }
}

export function saveStackSecret(hostID: string, stack: string, name: string, value: string) {
  return stackAction(hostID, stack, name, () => putStackSecret(hostID, stack, name, value));
}

export function removeStackSecret(hostID: string, stack: string, name: string) {
  return stackAction(hostID, stack, name, () => deleteStackSecret(hostID, stack, name));
}
