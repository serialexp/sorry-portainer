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
  /** Why the status is unknown, when the registry was asked and failed. */
  failure?: ImageFailure;
  /** For a refused image, the saved login it would use (set by the master). */
  login_key?: string;
  /** For a refused image, whether a login is saved and on this host (set by the master). */
  login_state?: LoginState;
};

/**
 * Why a registry refused, as the master explains it: no saved login matches
 * the image ("none"), one does but this host doesn't have it yet
 * ("not_on_host"), or the host has it and the registry refused it ("refused").
 */
export type LoginState = "none" | "not_on_host" | "refused";

/**
 * "denied": the registry refused the credentials or asked for some (Docker
 * Hub also says so for a repository that does not exist); "not_found": no
 * such tag; "unreachable": no answer, a timeout, or a server error.
 */
export type ImageFailure = "denied" | "not_found" | "unreachable";

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

/** The steps an update reports as it starts them (see protocol.UpdatePulling). */
export type UpdateStep = "pulling" | "recreating";

/**
 * Reads the update's server-sent events: each step for onStep, then the
 * result, or the failure the server reports after the stream started.
 */
async function readUpdateEvents(
  body: ReadableStream<Uint8Array>,
  onStep: (step: UpdateStep) => void,
): Promise<StackOperation> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    for (let end = buffer.indexOf("\n\n"); end >= 0; end = buffer.indexOf("\n\n")) {
      const block = buffer.slice(0, end);
      buffer = buffer.slice(end + 2);
      let event = "message";
      let data = "";
      for (const line of block.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data += line.slice(5).trim();
      }
      if (event === "step") onStep((JSON.parse(data) as { step: UpdateStep }).step);
      else if (event === "result") {
        await reader.cancel();
        return JSON.parse(data) as StackOperation;
      } else if (event === "error") {
        await reader.cancel();
        throw new Error((JSON.parse(data) as { message: string }).message);
      }
    }
  }
  throw new Error("The connection ended before the update finished; check the stack before trying again.");
}

/**
 * Pulls a service's image and, when the stack runs, recreates the service.
 * onStep is told each step as the agent starts it.
 */
export async function updateService(
  hostID: string,
  name: string,
  service: string,
  onStep: (step: UpdateStep) => void = () => {},
  request: typeof fetch = fetch,
): Promise<StackOperation> {
  const response = await request(`${endpoint(hostID, name)}/update`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ service }),
  });
  const failed = `Updating ${service} failed`;
  if (!response.ok) throw await failure(response, failed);
  const streamed = response.headers?.get("Content-Type")?.startsWith("text/event-stream") && response.body;
  const result = streamed ? await readUpdateEvents(streamed, onStep) : ((await response.json()) as StackOperation);
  const output = terminalText(result.output ?? "");
  if (!result.success) throw new StackOperationError(`${failed}.`, output);
  return { ...result, output };
}

/** The row's text while an update runs: the design's two steps. */
export function updateStepText(step: UpdateStep | ""): string {
  if (step === "pulling") return "Pulling new image…";
  if (step === "recreating") return "Recreating container…";
  return "Updating…";
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

/** The line for an image a registry refused, once the master said why; undefined without its answer. */
function loginStatusLine(img: ServiceImage, hostID: string): ImageStatusLine | undefined {
  const registry = img.registry || "the registry";
  const key = img.login_key ?? "";
  switch (img.login_state) {
    case "none":
      return {
        text: "No login for this registry",
        tone: "muted",
        title:
          `${registry} refused the request and there is no saved login for it. Add one in Registry logins` +
          (registry === "docker.io"
            ? ", or check the name: Docker Hub answers the same way when a repository doesn’t exist."
            : "."),
      };
    case "not_on_host":
      return {
        text: "Login not on this host yet",
        tone: "muted",
        title: `A login for ${key} is saved, but ${hostID} doesn’t have it yet (offline, delivery failed, or rebooted while the store was locked). Checked again once it arrives.`,
      };
    case "refused":
      return {
        text: "Login refused",
        tone: "muted",
        title: `${hostID} has the login for ${key}, but ${registry} refused it. The token may be expired or missing pull rights. Replace it in Registry logins.`,
      };
  }
  return undefined;
}

/** The line under a service's image: whether it runs what its tag points to now. */
export function imageStatusLine(img: ServiceImage, now: number, hostID = "this host"): ImageStatusLine {
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
      switch (img.failure) {
        case "denied":
          return (
            loginStatusLine(img, hostID) ?? {
              text: "No access",
              tone: "muted",
              title: img.error || "The registry refused access to the image",
            }
          );
        case "not_found":
          return { text: "Tag not found", tone: "muted", title: img.error || "The registry has no such tag" };
        default:
          return { text: "Couldn’t check", tone: "muted", title: img.error || "The registry could not be reached" };
      }
    case "pinned":
      return { text: "Pinned by digest", tone: "muted", title: "The image is named by digest, so it cannot change" };
    case "local":
      return { text: "Local image", tone: "muted", title: img.error || "The image is built or kept on this host" };
    default:
      return { text: "Not checked", tone: "muted", title: img.error || "" };
  }
}

/**
 * The Services header's check line: when the images were checked, or the
 * first check that failed: a registry that refused access, a tag it does not
 * have, or one that could not be reached.
 */
export function checkedText(images: ServiceImage[], now: number): string {
  const failed = images.find((img) => img.status === "unknown");
  if (failed) {
    const where = failed.registry || "the registry";
    const login = failed.failure === "denied" ? loginCheckedText(failed, where) : "";
    if (login) return failed.checked_at ? `${login} · checked ${timeAgo(now - failed.checked_at)}` : login;
    let text = `Couldn’t reach ${where}`;
    if (failed.failure === "denied") text = `${where} refused access`;
    if (failed.failure === "not_found") text = `Tag ${imageTag(failed.image)} not found on ${where}`;
    return failed.checked_at ? `${text} · last checked ${timeAgo(now - failed.checked_at)}` : text;
  }
  const times = images.flatMap((img) => (img.checked_at ? [img.checked_at] : []));
  if (!times.length) return "";
  return `Images checked ${timeAgo(now - Math.min(...times))}`;
}

/** The header's words for a refused image the master explained; "" without its answer. */
function loginCheckedText(img: ServiceImage, where: string): string {
  switch (img.login_state) {
    case "none":
      return `No login for ${where}`;
    case "not_on_host":
      return `Waiting for the ${img.login_key} login`;
    case "refused":
      return `${where} refused the login`;
  }
  return "";
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
