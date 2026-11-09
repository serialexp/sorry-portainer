import { UnauthorizedError } from "./fleet-api.ts";
import { splitImageRef } from "./image-ref.ts";
import { type StackOperation, StackOperationError, terminalText } from "./stack-api.ts";

/** How a service's image compares with what its tag points to now (see protocol.ServiceImage). */
export type ImageStatus = "current" | "behind" | "unknown" | "pinned" | "local" | "unchecked";

export type ServiceImage = {
  service: string;
  image?: string;
  status: ImageStatus;
  /** The registry, as people write it ("docker.io"). */
  registry?: string;
  running_digest?: string;
  latest_digest?: string;
  /** When the newer image was built, unix seconds; only while behind. */
  latest_created?: number;
  /** When the registry last answered, unix seconds; absent if it never did. */
  checked_at?: number;
  error?: string;
};

export type StackImages = { name: string; services: ServiceImage[] };

async function failure(response: Response, fallback: string): Promise<Error> {
  const text = (await response.text()).trim();
  if (response.status === 401) return new UnauthorizedError(text || fallback);
  return new Error(text || `${fallback} (${response.status}).`);
}

function endpoint(hostID: string, name: string): string {
  return `/api/hosts/${encodeURIComponent(hostID)}/stacks/${encodeURIComponent(name)}`;
}

/**
 * Asks whether the stack's images are still what their tags point to. The
 * agent answers from its cache unless force is set (Check now).
 */
export async function getStackImages(
  hostID: string,
  name: string,
  force: boolean,
  request: typeof fetch = fetch,
): Promise<StackImages> {
  const response = await request(`${endpoint(hostID, name)}/images${force ? "?force=1" : ""}`);
  if (!response.ok) throw await failure(response, "Could not check images");
  return (await response.json()) as StackImages;
}

/** Pulls a service's image and, when the stack runs, recreates the service. */
export async function updateService(
  hostID: string,
  name: string,
  service: string,
  request: typeof fetch = fetch,
): Promise<StackOperation> {
  const response = await request(`${endpoint(hostID, name)}/update`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ service }),
  });
  const failed = `Updating ${service} failed`;
  if (!response.ok) throw await failure(response, failed);
  const result = (await response.json()) as StackOperation;
  const output = terminalText(result.output ?? "");
  if (!result.success) throw new StackOperationError(`${failed}.`, output);
  return { ...result, output };
}

/** "just now", "4 min ago", "1 hour ago", "3 days ago", "2 weeks ago", "5 months ago", "2 years ago". */
export function timeAgo(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const units: [number, string][] = [
    [365 * 86400, "year"],
    [30 * 86400, "month"],
    [7 * 86400, "week"],
    [86400, "day"],
    [3600, "hour"],
  ];
  for (const [size, unit] of units) {
    if (s >= size) {
      const n = Math.floor(s / size);
      return `${n} ${unit}${n === 1 ? "" : "s"} ago`;
    }
  }
  if (s >= 60) return `${Math.floor(s / 60)} min ago`;
  return "just now";
}

/** "sha256:9b1e2f07c3aa": the algorithm and twelve hex digits. */
export function shortDigest(digest: string | undefined): string {
  if (!digest) return "";
  const at = digest.indexOf(":");
  return at < 0 ? digest.slice(0, 12) : digest.slice(0, at + 13);
}

/** The tag a reference names; untagged means "latest". */
export function imageTag(ref: string | undefined): string {
  if (!ref) return "";
  if (ref.includes("@")) return "";
  return splitImageRef(ref).tag || "latest";
}

/** Whether a status comes from asking the registry (and so changes on Check now). */
export function registryChecked(status: ImageStatus): boolean {
  return status === "current" || status === "behind" || status === "unknown";
}

export type ImageStatusLine = { text: string; tone: "ok" | "warn" | "muted"; title: string };

/** The line under a service's image: whether it runs what its tag points to now. */
export function imageStatusLine(img: ServiceImage, now: number): ImageStatusLine {
  const image = img.image ?? "";
  switch (img.status) {
    case "current":
      return { text: "Up to date", tone: "ok", title: `Running the image ${image} points to now` };
    case "behind": {
      const built = img.latest_created ? ` · built ${timeAgo(now - img.latest_created)}` : "";
      const title = img.running_digest
        ? `${image} now points to a different image than the one running`
        : `${image} is not on this host yet`;
      return { text: `Newer ${imageTag(image)}${built}`, tone: "warn", title };
    }
    case "unknown":
      return { text: "Couldn’t check", tone: "muted", title: img.error || "The registry could not be reached" };
    case "pinned":
      return { text: "Pinned by digest", tone: "muted", title: "The image is named by digest, so it cannot change" };
    case "local":
      return { text: "Local image", tone: "muted", title: img.error || "The image is built or kept on this host" };
    default:
      return { text: "Not checked", tone: "muted", title: img.error || "" };
  }
}

/** The Services header's check line: when the images were checked, or which registry could not be reached. */
export function checkedText(images: ServiceImage[], now: number): string {
  const failed = images.find((img) => img.status === "unknown");
  if (failed) {
    const where = failed.registry || "the registry";
    return failed.checked_at
      ? `Couldn’t reach ${where} · last checked ${timeAgo(now - failed.checked_at)}`
      : `Couldn’t reach ${where}`;
  }
  const times = images.flatMap((img) => (img.checked_at ? [img.checked_at] : []));
  if (!times.length) return "";
  return `Images checked ${timeAgo(now - Math.min(...times))}`;
}

/** "1 update", "3 updates"; empty without any. */
export function updatesLabel(images: ServiceImage[]): string {
  const behind = images.filter((img) => img.status === "behind").length;
  if (!behind) return "";
  return behind === 1 ? "1 update" : `${behind} updates`;
}

/** The confirm row's sentence and button for updating a service. */
export function updatePrompt(service: string, image: string, stackRuns: boolean): { text: string; label: string } {
  return stackRuns
    ? {
        text: `Pull the newest ${image} and recreate ${service}. Other services keep running; ${service} is down for a few seconds.`,
        label: `Update ${service}`,
      }
    : {
        text: `Pull the newest ${image}. The stack is stopped, so ${service} uses it the next time the stack starts.`,
        label: "Pull image",
      };
}

/** "running sha256:… → latest now sha256:… · built 3 days ago" for the confirm row. */
export function updateDetail(img: ServiceImage, now: number): string {
  const running = img.running_digest ? `running ${shortDigest(img.running_digest)}` : "not on this host";
  const built = img.latest_created ? ` · built ${timeAgo(now - img.latest_created)}` : "";
  return `${running} → ${imageTag(img.image)} now ${shortDigest(img.latest_digest)}${built}`;
}
