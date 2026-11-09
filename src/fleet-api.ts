// Client for the master's session, host and container routes.

import type { HealthFacts } from "./container-health.ts";
import type { HostSecretInfo } from "./secrets-ui";

export type Host = { host_id: string; prefix: string; hostname: string; engine_version: string } & HostSecretInfo;
/** A container on a host, with its health and restart facts (see container-health.ts). */
export type Container = HealthFacts & {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
};
/** One container's use of a volume; `mode` is "rw" or "ro". */
export type VolumeMount = { container_id: string; container: string; destination: string; mode: string; state: string };
export type Volume = {
  name: string;
  driver: string;
  mountpoint: string;
  mounts?: VolumeMount[];
  /** True when a container outside this agent's prefix mounts the volume. */
  mounted_outside_prefix?: boolean;
  /** Size of the volume's files at `size_measured_at`, from the agent's background scan; absent until the first scan. */
  size_bytes?: number;
  size_measured_at?: string;
  /** True when some files could not be read, so `size_bytes` is a lower bound. */
  size_partial?: boolean;
};
export type VolumeDetail = Volume & { created?: string; labels?: Record<string, string> };
/** One container of this agent that runs an image. */
export type ImageUse = { container_id: string; container: string; state: string };
export type Image = {
  id: string;
  tags?: string[];
  size: number;
  created: number;
  containers?: ImageUse[];
  /** True when a container outside this agent's prefix runs the image. */
  used_outside_prefix?: boolean;
};
export type ImageLayer = { created_by: string; size: number };
export type ImageDetail = Image & {
  platform: string;
  command?: string[];
  entrypoint?: string[];
  working_dir?: string;
  ports?: string[];
  env?: string[];
  layers?: ImageLayer[];
};
export type ContainerOperation = "start" | "stop";

/** Thrown when the master answers 401: no session, or a wrong password. */
export class UnauthorizedError extends Error {}

/** Thrown when sign-in cannot reach the master at all. */
export class MasterUnreachableError extends Error {}

// What a missing master looks like: the request fails, or a development
// proxy in front of it answers that it has no upstream.
const unreachableStatuses = new Set([502, 503, 504]);

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

/**
 * Starts a session; the master sets it as an HttpOnly cookie. Too many
 * failed attempts get the master's "try again in" message.
 */
export async function login(password: string, request: typeof fetch = fetch): Promise<void> {
  let response: Response;
  try {
    response = await request("/api/session/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password }),
    });
  } catch (cause) {
    throw new MasterUnreachableError(cause instanceof Error ? cause.message : "The master could not be reached.");
  }
  if (response.status === 401) throw new UnauthorizedError("Wrong password.");
  if (unreachableStatuses.has(response.status))
    throw new MasterUnreachableError(`The master could not be reached (${response.status}).`);
  if (!response.ok) throw await failure(response, "Could not sign in");
}

/** Ends the session on the master. */
export async function logout(request: typeof fetch = fetch): Promise<void> {
  const response = await request("/api/session/logout", { method: "POST" });
  if (!response.ok) throw await failure(response, "Could not sign out");
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

export function getVolume(hostID: string, name: string, request: typeof fetch = fetch): Promise<VolumeDetail> {
  return getJSON(
    `${hostEndpoint(hostID, "volumes")}/${encodeURIComponent(name)}`,
    "Could not load the volume",
    request,
  );
}

export function getImage(hostID: string, id: string, request: typeof fetch = fetch): Promise<ImageDetail> {
  return getJSON(`${hostEndpoint(hostID, "images")}/${encodeURIComponent(id)}`, "Could not load the image", request);
}

export function listImages(hostID: string, request: typeof fetch = fetch): Promise<Image[]> {
  return getJSON(hostEndpoint(hostID, "images"), "Could not load images", request);
}

/** Removes a volume of the host. The host refuses one that a container still mounts. */
export async function deleteVolume(hostID: string, name: string, request: typeof fetch = fetch): Promise<void> {
  const response = await request(`${hostEndpoint(hostID, "volumes")}/${encodeURIComponent(name)}`, {
    method: "DELETE",
  });
  if (!response.ok) throw await failure(response, "Could not remove the volume");
}

/** Removes an image of the host. The host refuses one that a container uses, or one with several tags. */
export async function deleteImage(hostID: string, id: string, request: typeof fetch = fetch): Promise<void> {
  const response = await request(`${hostEndpoint(hostID, "images")}/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
  if (!response.ok) throw await failure(response, "Could not remove the image");
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
