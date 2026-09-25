// Client for the master's secret store and per-stack secret routes. Secret
// values only travel from the browser to the master; the API never returns
// them.

export type StoreState = "uninitialized" | "locked" | "unlocked";
export type SecretEntry = { name: string; size: number; updated_at: string };
export type SecretSyncResult = {
  stack: string;
  changed?: string[];
  removed?: string[];
  restarted?: string[];
  problems?: string[];
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

async function failure(response: Response, fallback: string): Promise<Error> {
  const text = (await response.text()).trim();
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

function passphraseRequest(action: "initialize" | "unlock", passphrase: string, request: typeof fetch) {
  return stateRequest(
    action,
    { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ passphrase }) },
    action === "initialize" ? "Could not create the secret store" : "Could not unlock the secret store",
    request,
  );
}

export async function initializeStore(passphrase: string, request: typeof fetch = fetch): Promise<StoreState> {
  if (passphrase.length < minPassphraseLength)
    throw new Error(`Use a passphrase of at least ${minPassphraseLength} characters.`);
  return passphraseRequest("initialize", passphrase, request);
}

export function unlockStore(passphrase: string, request: typeof fetch = fetch): Promise<StoreState> {
  return passphraseRequest("unlock", passphrase, request);
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
