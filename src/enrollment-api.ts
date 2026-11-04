// Client for the master's host enrollment routes. A token's secret is only
// in the answer that creates it; the token list never includes it.

// The extension lets Node's test runner load this module directly.
import { UnauthorizedError } from "./fleet-api.ts";

export type EnrolledHost = {
  host_id: string;
  prefix: string;
  enrolled_at: string;
  current_not_after: string;
  /** A renewed certificate waits for the agent to connect with it. */
  pending: boolean;
  online: boolean;
};
export type OutstandingToken = { host_id: string; expires_at: string };
export type IssuedToken = {
  token: string;
  expires_at: string;
  command: string;
  /** The host is already enrolled; the command replaces its agent's configuration. */
  reenroll: boolean;
};

/** Same rule as the master's protocol.ValidHostID. */
export const hostIDPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

/** Token lifetimes offered in the form; the master accepts 1m to 24h. */
export const tokenLifetimes = [
  { value: "15m", label: "15 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "4h", label: "4 hours" },
  { value: "24h", label: "24 hours" },
] as const;
export type TokenLifetime = (typeof tokenLifetimes)[number]["value"];

/**
 * Agents renew between 37 and 30 days before their certificate expires
 * (agentcred.RenewBefore plus up to MaxJitter), so one with less than 30
 * days left has missed its renewal.
 */
export const renewalDeadlineDays = 30;
const day = 24 * 60 * 60 * 1000;

export type CertificateState = "valid" | "renewing" | "overdue" | "expired";

export function certificateState(
  host: Pick<EnrolledHost, "current_not_after" | "pending">,
  now: Date,
): CertificateState {
  const left = Date.parse(host.current_not_after) - now.getTime();
  if (!(left > 0)) return "expired";
  if (host.pending) return "renewing";
  return left < renewalDeadlineDays * day ? "overdue" : "valid";
}

/** Thrown when the master has no enrollment service (HTTP 503). */
export class EnrollmentUnavailableError extends Error {}

async function failure(response: Response, fallback: string): Promise<Error> {
  const text = (await response.text()).trim();
  if (response.status === 401) return new UnauthorizedError(text || fallback);
  if (response.status === 503) return new EnrollmentUnavailableError(text || fallback);
  return new Error(text || `${fallback} (${response.status}).`);
}

async function getJSON<T>(path: string, fallback: string, request: typeof fetch): Promise<T> {
  const response = await request(path);
  if (!response.ok) throw await failure(response, fallback);
  return (await response.json()) as T;
}

async function remove(path: string, fallback: string, request: typeof fetch): Promise<void> {
  const response = await request(path, { method: "DELETE" });
  if (!response.ok) throw await failure(response, fallback);
}

export function listEnrolledHosts(request: typeof fetch = fetch): Promise<EnrolledHost[]> {
  return getJSON("/api/enrollment/hosts", "Could not load enrolled hosts", request);
}

export function listTokens(request: typeof fetch = fetch): Promise<OutstandingToken[]> {
  return getJSON("/api/enrollment/tokens", "Could not load enrollment tokens", request);
}

/** Creates a token for hostID; an outstanding token for it is replaced. */
export async function createToken(
  hostID: string,
  ttl: TokenLifetime,
  request: typeof fetch = fetch,
): Promise<IssuedToken> {
  if (!hostIDPattern.test(hostID))
    throw new Error("Use 1–128 letters, digits, '.', '_' or '-' for the host ID, starting with a letter or digit.");
  const response = await request("/api/enrollment/tokens", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ host_id: hostID, ttl }),
    cache: "no-store",
  });
  if (!response.ok) throw await failure(response, "Could not create a token");
  return (await response.json()) as IssuedToken;
}

export function revokeToken(hostID: string, request: typeof fetch = fetch): Promise<void> {
  return remove(`/api/enrollment/tokens/${encodeURIComponent(hostID)}`, "Could not revoke the token", request);
}

/** Removes the host from the registry, revokes its token and disconnects it. */
export function removeHost(hostID: string, request: typeof fetch = fetch): Promise<void> {
  return remove(`/api/enrollment/hosts/${encodeURIComponent(hostID)}`, "Could not remove the host", request);
}
