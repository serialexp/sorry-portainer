import { createStore } from "solid-js/store";
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

const [secrets, setSecrets] = createStore<SecretsState>({
  store: "unknown",
  storeBusy: false,
  storeError: "",
  stacks: {},
});

export { secrets };

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
  try {
    setSecrets({ store: await getStoreState(), storeError: "" });
  } catch (cause) {
    if (cause instanceof SecretsUnavailableError) setSecrets({ store: "unavailable", storeError: "" });
    else setSecrets("storeError", message(cause, "Could not read the secret store state."));
  }
}

async function storeAction(action: () => Promise<StoreState>, fallback: string): Promise<boolean> {
  if (secrets.storeBusy) return false;
  setSecrets({ storeBusy: true, storeError: "" });
  try {
    const state = await action();
    setSecrets("store", state);
    return true;
  } catch (cause) {
    setSecrets("storeError", message(cause, fallback));
    return false;
  } finally {
    setSecrets("storeBusy", false);
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
  ensureStack(key);
  setSecrets("stacks", key, { loading: true, error: "" });
  try {
    const result = await listStackSecrets(hostID, stack);
    setSecrets("store", result.state);
    setSecrets("stacks", key, { entries: result.secrets ?? [], delivery: result.delivery });
  } catch (cause) {
    if (cause instanceof SecretsUnavailableError) setSecrets("store", "unavailable");
    setSecrets("stacks", key, "error", message(cause, "Could not load stack secrets."));
  } finally {
    setSecrets("stacks", key, "loading", false);
  }
}

async function stackAction(hostID: string, stack: string, name: string, action: () => Promise<Delivery>) {
  const key = stackKey(hostID, stack);
  ensureStack(key);
  if (secrets.stacks[key].busy) return false;
  setSecrets("stacks", key, { busy: name, error: "" });
  try {
    setSecrets("stacks", key, "delivery", await action());
    return true;
  } catch (cause) {
    setSecrets("stacks", key, "error", message(cause, "The secret change failed."));
    return false;
  } finally {
    setSecrets("stacks", key, "busy", "");
    // Refresh names and the store state (a 423 means it was locked meanwhile).
    await loadStackSecrets(hostID, stack);
  }
}

export function saveStackSecret(hostID: string, stack: string, name: string, value: string) {
  return stackAction(hostID, stack, name, () => putStackSecret(hostID, stack, name, value));
}

export function removeStackSecret(hostID: string, stack: string, name: string) {
  return stackAction(hostID, stack, name, () => deleteStackSecret(hostID, stack, name));
}
