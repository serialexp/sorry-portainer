// Client for the master's registry logins (/api/registries). Passwords
// travel from the browser to the master; the one way back is revealLogin,
// which needs the secret store passphrase again for each password. Keys
// contain "/", so every request names a login in its body, never the path.

import { UnauthorizedError } from "./fleet-api.ts";
import {
  SecretsUnavailableError,
  type StoreState,
  TooManyAttemptsError,
  WrongPassphraseError,
  retryAfterSeconds,
} from "./secrets-api.ts";

/** Why a sign-in test failed (see protocol.RegistryTest*). */
export type LoginTestFailure = "denied" | "unreachable" | "tls";

/** The last sign-in test of a login, kept with it on the master. */
export type LoginTest = {
  state: "ok" | "failed";
  failure?: LoginTestFailure;
  message?: string;
  host: string;
  at: string;
};

export type RegistryLogin = {
  /** A registry ("ghcr.io") or a namespace in one ("docker.io/myorg"). */
  key: string;
  username: string;
  updated_at: string;
  last_test?: LoginTest;
};

/** How far a host is with the current set of logins. */
export type HostLoginState = "current" | "sending" | "failed" | "missing" | "offline";

export type LoginHostStatus = {
  host: string;
  online: boolean;
  /** The set the host holds; empty when it has none. */
  set_id: string;
  state: HostLoginState;
  /** When it acknowledged the current set; absent when it reported it at connect. */
  at?: string;
  error?: string;
  retry_at?: string;
  last_seen?: string;
};

export type RegistryLogins = {
  state: StoreState;
  set_id: string;
  logins: RegistryLogin[];
  hosts: LoginHostStatus[];
};

/** A sign-in test's answer; no_agent when no online host answered. */
export type LoginTestResult = {
  state: "ok" | "failed" | "no_agent";
  failure?: LoginTestFailure;
  message?: string;
  host?: string;
};

export type LoginSaved = { login: RegistryLogin; test: LoginTestResult };

/** The limit on a password or token, as the master enforces it. */
export const maxLoginPassword = 4096;

/** Thrown when the secret store is locked (HTTP 423). */
export class LoginsLockedError extends Error {}

/** Thrown when adding a key that already has a login (HTTP 409). */
export class LoginExistsError extends Error {}

async function failure(response: Response, fallback: string): Promise<Error> {
  const text = (await response.text()).trim();
  switch (response.status) {
    case 401:
      return new UnauthorizedError(text || fallback);
    case 503:
      return new SecretsUnavailableError(text || fallback);
    case 423:
      return new LoginsLockedError(text || "the secret store is locked");
    case 409:
      return new LoginExistsError(text || fallback);
    case 403:
      // A reveal with a wrong passphrase, reported as an unlock's is.
      return new WrongPassphraseError(text || "wrong passphrase");
    case 429: {
      const wait = retryAfterSeconds(response.headers?.get("Retry-After"));
      const message = text || "too many passphrase attempts";
      return wait === undefined ? new Error(message) : new TooManyAttemptsError(message, wait);
    }
  }
  return new Error(text || `${fallback} (${response.status}).`);
}

function post(path: string, body: unknown, request: typeof fetch, init: RequestInit = {}) {
  return request(`/api/registries${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    ...init,
  });
}

export async function listLogins(request: typeof fetch = fetch): Promise<RegistryLogins> {
  const response = await request("/api/registries");
  if (!response.ok) throw await failure(response, "Could not load registry logins");
  const body = (await response.json()) as RegistryLogins;
  return { ...body, logins: body.logins ?? [], hosts: body.hosts ?? [] };
}

/**
 * Saves a new login (replace false) or a new username and password for a
 * saved one. Replacing with an empty password keeps the stored password. The
 * master tests the login on an online host before answering.
 */
export async function saveLogin(
  login: { key: string; username: string; password: string },
  replace: boolean,
  request: typeof fetch = fetch,
): Promise<LoginSaved> {
  const response = await post(replace ? "/replace" : "", login, request);
  if (!response.ok) throw await failure(response, "Could not save the login");
  return (await response.json()) as LoginSaved;
}

export async function deleteLogin(key: string, request: typeof fetch = fetch): Promise<void> {
  const response = await post("/delete", { key }, request);
  if (!response.ok) throw await failure(response, "Could not delete the login");
}

/** Fetches one stored password. The master checks the passphrase again and the answer is not cached. */
export async function revealLogin(key: string, passphrase: string, request: typeof fetch = fetch): Promise<string> {
  if (!passphrase) throw new Error("Enter the secret store passphrase.");
  const response = await post("/reveal", { key, passphrase }, request, { cache: "no-store" });
  if (!response.ok) throw await failure(response, "Could not show the password");
  return ((await response.json()) as { password: string }).password;
}

/** Sends the current set to host now, skipping its backoff. */
export async function retryHost(host: string, request: typeof fetch = fetch): Promise<void> {
  const response = await post("/retry", { host }, request);
  if (!response.ok) throw await failure(response, `Could not retry ${host}`);
}
