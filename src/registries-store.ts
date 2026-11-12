import { batch } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import { onSessionEnd, sessionEnded } from "./fleet-store";
import {
  deleteLogin,
  LoginExistsError,
  type LoginHostStatus,
  LoginsLockedError,
  listLogins,
  type RegistryLogin,
  retryHost,
  revealLogin,
  saveLogin,
} from "./registries-api";
import { type Banner, checkLoginForm, deletedBanner, loginExistsMessage, savedBanner } from "./registry-logins";
import { retryMessage, unlockFailure } from "./secret-unlock.ts";
import { refreshStoreState, secrets } from "./secrets-store";
import { SecretsUnavailableError, TooManyAttemptsError } from "./secrets-api";

// The Registry logins page. Passwords are never kept here: the form's
// password field and a shown password live in the components that use them.

export type LoginFormState = {
  mode: "add" | "replace";
  key: string;
  username: string;
  error: string;
};

type RegistriesState = {
  /** "unavailable" when the master has no secret store configured. */
  phase: "idle" | "loading" | "ready" | "unavailable";
  error: string;
  logins: RegistryLogin[];
  hosts: LoginHostStatus[];
  setID: string;
  form: LoginFormState | null;
  saving: boolean;
  banner: Banner | null;
  /** The row with a prompt open: the passphrase to show it, or the delete confirmation. */
  row: { key: string; mode: "ask" | "delete" } | null;
  /** What is in progress: "delete:<key>" or "retry:<host>". */
  busy: string;
  /** Unix seconds the page's relative times are read against. */
  now: number;
};

const initial = (): RegistriesState => ({
  phase: "idle",
  error: "",
  logins: [],
  hosts: [],
  setID: "",
  form: null,
  saving: false,
  banner: null,
  row: null,
  busy: "",
  now: Math.floor(Date.now() / 1000),
});

const [registries, setRegistries] = createStore<RegistriesState>(initial());

export { registries };

// Bumped when the session ends or the page closes, so answers to earlier
// requests are dropped. Loads also drop older loads.
let generation = 0;
let loadSeq = 0;

onSessionEnd(() => {
  ++generation;
  setRegistries(reconcile(initial()));
});

function message(cause: unknown, fallback: string): string {
  return cause instanceof Error ? cause.message : fallback;
}

/** Whether a failure was the store being locked meanwhile; picks up its state if so. */
function lockedMeanwhile(cause: unknown): boolean {
  if (!(cause instanceof LoginsLockedError)) return false;
  void refreshStoreState();
  return true;
}

export async function loadLogins() {
  const started = generation;
  const seq = ++loadSeq;
  if (registries.phase === "idle") setRegistries("phase", "loading");
  try {
    const result = await listLogins();
    if (started !== generation || seq !== loadSeq) return;
    batch(() => {
      // Reconciled by key, so open rows keep their elements across polls.
      setRegistries("logins", reconcile(result.logins, { key: "key" }));
      setRegistries("hosts", reconcile(result.hosts, { key: "host" }));
      setRegistries({ phase: "ready", error: "", setID: result.set_id, now: Math.floor(Date.now() / 1000) });
    });
    // The sidebar and the banners read the store state from the secrets store.
    if (result.state !== secrets.store) void refreshStoreState();
  } catch (cause) {
    if (started !== generation || seq !== loadSeq || sessionEnded(cause)) return;
    if (cause instanceof SecretsUnavailableError) setRegistries({ phase: "unavailable", error: "" });
    else
      setRegistries({
        phase: registries.phase === "loading" ? "ready" : registries.phase,
        error: message(cause, "Could not load registry logins."),
      });
  }
}

let poller: ReturnType<typeof setInterval> | undefined;

/**
 * Loads the page and keeps it current while it is open: hosts receive sets
 * in the background, so their rows change without anything done here. One
 * small read every 5 s while the tab is visible.
 */
export function openRegistriesPage(): () => void {
  void loadLogins();
  clearInterval(poller);
  poller = setInterval(() => {
    if (document.visibilityState === "visible") void loadLogins();
  }, 5000);
  return () => {
    clearInterval(poller);
    poller = undefined;
    ++generation;
    setRegistries({ form: null, saving: false, banner: null, row: null, busy: "" });
  };
}

export function openAddForm() {
  setRegistries({ form: { mode: "add", key: "", username: "", error: "" }, banner: null, row: null });
}

export function openReplaceForm(login: RegistryLogin) {
  setRegistries({
    form: { mode: "replace", key: login.key, username: login.username, error: "" },
    banner: null,
    row: null,
  });
}

export function closeForm() {
  if (!registries.saving) setRegistries("form", null);
}

export function editForm(field: "key" | "username", value: string) {
  if (registries.form) setRegistries("form", { [field]: value, error: "" });
}

export function clearFormError() {
  if (registries.form?.error) setRegistries("form", "error", "");
}

/** Saves the open form with password; true when saved (the test may still have failed). */
export async function submitForm(password: string): Promise<boolean> {
  const form = registries.form;
  if (!form || registries.saving) return false;
  const checked = checkLoginForm(
    { ...form, password },
    registries.logins.map((login) => login.key),
  );
  if (checked.error) {
    setRegistries("form", "error", checked.error);
    return false;
  }
  const started = generation;
  setRegistries({ saving: true });
  try {
    const saved = await saveLogin(
      { key: checked.key, username: form.username.trim(), password },
      form.mode === "replace",
    );
    if (started !== generation) return false;
    setRegistries({ form: null, banner: savedBanner(saved.login.key, saved.test) });
    void loadLogins();
    return true;
  } catch (cause) {
    if (started !== generation || sessionEnded(cause)) return false;
    let error = message(cause, "Could not save the login.");
    if (cause instanceof LoginExistsError && form.mode === "add") error = loginExistsMessage(checked.key);
    if (lockedMeanwhile(cause)) error = "The secret store was locked. Unlock it and save again.";
    setRegistries("form", "error", error);
    return false;
  } finally {
    if (started === generation) setRegistries("saving", false);
  }
}

export function dismissBanner() {
  setRegistries("banner", null);
}

export function askRow(key: string, mode: "ask" | "delete") {
  setRegistries({ row: { key, mode }, form: null });
}

export function closeRow() {
  setRegistries("row", null);
}

export async function removeLogin(key: string) {
  if (registries.busy) return;
  const started = generation;
  setRegistries("busy", `delete:${key}`);
  try {
    await deleteLogin(key);
    if (started !== generation) return;
    setRegistries({ row: null, banner: deletedBanner });
    void loadLogins();
  } catch (cause) {
    if (started !== generation || sessionEnded(cause)) return;
    lockedMeanwhile(cause);
    setRegistries({ row: null, error: message(cause, "Could not delete the login.") });
  } finally {
    if (started === generation) setRegistries("busy", "");
  }
}

export async function retryNow(host: string) {
  if (registries.busy) return;
  const started = generation;
  setRegistries("busy", `retry:${host}`);
  try {
    await retryHost(host);
    if (started === generation) await loadLogins();
  } catch (cause) {
    if (started !== generation || sessionEnded(cause)) return;
    lockedMeanwhile(cause);
    setRegistries("error", message(cause, `Could not retry ${host}.`));
  } finally {
    if (started === generation) setRegistries("busy", "");
  }
}

export type RevealResult = { value: string } | { error: string } | null;

/**
 * Fetches one password for showing. It is returned to the caller only and
 * never kept in this store. Answers null when the page closed or the session
 * ended meanwhile.
 */
export async function showPassword(key: string, passphrase: string): Promise<RevealResult> {
  const started = generation;
  try {
    const value = await revealLogin(key, passphrase);
    return started === generation ? { value } : null;
  } catch (cause) {
    if (started !== generation || sessionEnded(cause)) return null;
    lockedMeanwhile(cause);
    if (cause instanceof TooManyAttemptsError && cause.retryAfterSeconds > 0)
      return { error: retryMessage(cause.retryAfterSeconds) };
    return { error: unlockFailure(cause, Date.now()).error || "Could not show the password." };
  }
}
