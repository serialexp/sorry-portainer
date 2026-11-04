// Client for the master's session, host and container routes.

import type { HostSecretInfo } from "./secrets-ui";

export type Host = { host_id: string; prefix: string; hostname: string; engine_version: string } & HostSecretInfo;
export type Container = { id: string; name: string; image: string; state: string; status: string };
export type Volume = { name: string; driver: string; mountpoint: string };
export type Image = { id: string; tags?: string[]; size: number; created: number };
export type ContainerOperation = "start" | "stop";

/** Thrown when the master answers 401: no session, or a wrong password. */
export class UnauthorizedError extends Error {}

async function failure(response: Response, fallback: string): Promise<Error> {
  const text = (await response.text()).trim();
  if (response.status === 401) return new UnauthorizedError(text || fallback);
  return new Error(text || `${fallback} (${response.status}).`);
}

async function getJSON<T>(path: string, fallback: string, request: typeof fetch): Promise<T> {
  const response = await request(path);
  if (!response.ok) throw await failure(response, fallback);
  return (await response.json()) as T;
}

function hostEndpoint(hostID: string, operation: string): string {
  return `/api/hosts/${encodeURIComponent(hostID)}/${operation}`;
}

/** Starts a session; the master sets it as an HttpOnly cookie. */
export async function login(password: string, request: typeof fetch = fetch): Promise<void> {
  const response = await request("/api/session/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  });
  if (response.status === 401) throw new UnauthorizedError("Wrong password.");
  if (!response.ok) throw await failure(response, "Could not sign in");
}

export function listHosts(request: typeof fetch = fetch): Promise<Host[]> {
  return getJSON("/api/hosts", "Could not load hosts", request);
}

export function hostInfo(hostID: string, request: typeof fetch = fetch): Promise<Host> {
  return getJSON(hostEndpoint(hostID, "info"), "Could not load the host", request);
}

export function listContainers(hostID: string, request: typeof fetch = fetch): Promise<Container[]> {
  return getJSON(hostEndpoint(hostID, "containers"), "Could not load containers", request);
}

export function listVolumes(hostID: string, request: typeof fetch = fetch): Promise<Volume[]> {
  return getJSON(hostEndpoint(hostID, "volumes"), "Could not load volumes", request);
}

export function listImages(hostID: string, request: typeof fetch = fetch): Promise<Image[]> {
  return getJSON(hostEndpoint(hostID, "images"), "Could not load images", request);
}

export async function containerOperation(
  hostID: string,
  containerID: string,
  operation: ContainerOperation,
  request: typeof fetch = fetch,
): Promise<void> {
  const response = await request(hostEndpoint(hostID, operation), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ container_id: containerID }),
  });
  if (!response.ok) throw await failure(response, `Could not ${operation} the container`);
}
