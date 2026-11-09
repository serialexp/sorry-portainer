// Client for the master's secret store and per-stack secret routes. Secret
// values travel from the browser to the master. The one way back is
// revealStackSecret, which needs the passphrase again for each value.

import { UnauthorizedError } from "./fleet-api.ts";

export type StoreState = "uninitialized" | "locked" | "unlocked";
export type SecretEntry = { name: string; size: number; updated_at: string };
export type SecretSyncResult = {
  stack: string;
  changed?: string[];
  removed?: string[];
  restarted?: string[];
  problems?: string[];
  /** The stack was waiting for these secrets to come back after a reboot, and was started. */
  started?: boolean;
};
export type Delivery = {
  state: "delivered" | "pending" | "failed";
  at: string;
  error?: string;
  result?: SecretSyncResult;
};
export type StackSecrets = { state: StoreState; secrets: SecretEntry[]; delivery: Delivery | null };

export const secretNamePattern = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/;
export const minPassphraseLength = 12;
export const maxSecretBytes = 64 * 1024;

/** Thrown when the master has no secret store configured (HTTP 503). */
export class SecretsUnavailableError extends Error {}

/** Thrown when unlocking with a wrong passphrase (HTTP 403). */
export class WrongPassphraseError extends Error {}

/**
 * Thrown when the master refuses passphrase attempts for a while (HTTP 429).
 * `retryAfterSeconds` is the master's Retry-After value.
 */
export class TooManyAttemptsError extends Error {
  readonly retryAfterSeconds: number;
  constructor(message: string, retryAfterSeconds: number) {
    super(message);
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

/** The whole seconds in a Retry-After header, or undefined when it holds none. */
export function retryAfterSeconds(header: string | null | undefined): number | undefined {
  if (!header || !/^\d+$/.test(header.trim())) return undefined;
  return Number(header.trim());
}

async function failure(response: Response, fallback: string): Promise<Error> {
  const text = (await response.text()).trim();
  if (response.status === 401) return new UnauthorizedError(text || fallback);
  if (response.status === 503) return new SecretsUnavailableError(text || fallback);

  return new Error(text || `${fallback} (${response.status}).`);
}

async function stateRequest(path: string, init: RequestInit | undefined, fallback: string, request: typeof fetch) {
  const response = await request(`/api/secrets/${path}`, init);
  if (!response.ok) throw await failure(response, fallback);
  return ((await response.json()) as { state: StoreState }).state;
}

export function getStoreState(request: typeof fetch = fetch): Promise<StoreState> {
  return stateRequest("status", undefined, "Could not read the secret store state", request);
}

/**
 * Unlocks the store. A wrong passphrase throws WrongPassphraseError, and the
 * master's attempt limit throws TooManyAttemptsError with its wait.
 */

export async function initializeStore(passphrase: string, request: typeof fetch = fetch): Promise<StoreState> {
  if (passphrase.length < minPassphraseLength)
    throw new Error(`Use a passphrase of at least ${minPassphraseLength} characters.`);
  return stateRequest(
    "initialize",
    { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ passphrase }) },
    "Could not create the secret store",
    request,
  );
}

export async function unlockStore(passphrase: string, request: typeof fetch = fetch): Promise<StoreState> {
  const response = await request("/api/secrets/unlock", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ passphrase }),
  });
  if (response.status === 403) throw new WrongPassphraseError((await response.text()).trim() || "wrong passphrase");
  if (response.status === 429) {
    const text = (await response.text()).trim() || "too many passphrase attempts";
    const wait = retryAfterSeconds(response.headers?.get("Retry-After"));
    // Without a wait to count down, the master's message is all there is.
    throw wait === undefined ? new Error(text) : new TooManyAttemptsError(text, wait);
  }
  if (!response.ok) throw await failure(response, "Could not unlock the secret store");
  return ((await response.json()) as { state: StoreState }).state;
}

export function lockStore(request: typeof fetch = fetch): Promise<StoreState> {
  return stateRequest("lock", { method: "POST" }, "Could not lock the secret store", request);
}

function endpoint(hostID: string, stack: string, name?: string): string {
  const base = `/api/hosts/${encodeURIComponent(hostID)}/stacks/${encodeURIComponent(stack)}/secrets`;
  return name === undefined ? base : `${base}/${encodeURIComponent(name)}`;
}

export async function listStackSecrets(
  hostID: string,
  stack: string,
  request: typeof fetch = fetch,
): Promise<StackSecrets> {
  const response = await request(endpoint(hostID, stack));
  if (!response.ok) throw await failure(response, "Could not load stack secrets");
  return (await response.json()) as StackSecrets;
}

export async function putStackSecret(
  hostID: string,
  stack: string,
  name: string,
  value: string,
  request: typeof fetch = fetch,
): Promise<Delivery> {
  if (!secretNamePattern.test(name)) throw new Error("Invalid secret name.");
  if (!value) throw new Error("Enter a secret value.");
  if (new TextEncoder().encode(value).length > maxSecretBytes) throw new Error("Secret values are limited to 64 KiB.");
  const response = await request(endpoint(hostID, stack, name), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ value }),
  });
  if (!response.ok) throw await failure(response, "Could not save the secret");
  return ((await response.json()) as { delivery: Delivery }).delivery;
}

export async function deleteStackSecret(
  hostID: string,
  stack: string,
  name: string,
  request: typeof fetch = fetch,
): Promise<Delivery> {
  const response = await request(endpoint(hostID, stack, name), { method: "DELETE" });
  if (!response.ok) throw await failure(response, "Could not delete the secret");
  return ((await response.json()) as { delivery: Delivery }).delivery;
}

/**
 * Fetches one stored value. The master checks the passphrase again, so a
 * dashboard session alone cannot read values back. The answer is not cached.
 */
export async function revealStackSecret(
  hostID: string,
  stack: string,
  name: string,
  passphrase: string,
  request: typeof fetch = fetch,
): Promise<string> {
  if (!passphrase) throw new Error("Enter the secret store passphrase.");
  const response = await request(`${endpoint(hostID, stack, name)}/reveal`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ passphrase }),
    cache: "no-store",
  });
  if (response.status === 403) throw new Error("Wrong passphrase.");
  if (!response.ok) throw await failure(response, "Could not show the secret");
  return ((await response.json()) as { value: string }).value;
}
