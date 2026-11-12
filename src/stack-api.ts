import { UnauthorizedError } from "./fleet-api.ts";
import type { StackService } from "./stack-state.ts";

export type Stack = {
  name: string;
  project: string;
  status: string;
  version: number;
  compose_yaml?: string;
  /** Stack secrets the Compose file uses (single-stack reads only). */
  secrets?: string[];
  /** What the agent brings the stack back to after a reboot; absent if never started. */
  desired?: "up" | "down";
  /** Why a stack that should be up has not come up since the host booted. */
  waiting?: string;
  /** The repository whose pull a registry refused, when waiting for a registry login. */
  waiting_login?: string;
  /** The saved login that repository uses, if any (set by the master). */
  waiting_login_key?: string;
  /** Whether that login is saved and on the host (set by the master); see LoginState. */
  waiting_login_state?: "none" | "not_on_host" | "refused";
  /** The active revision's environment (single-stack reads and saves only). */
  environment?: Record<string, string>;
  /** Each declared service with its containers' state, in file order. */
  services?: StackService[];
};

export type { StackService } from "./stack-state.ts";

/**
 * Short text for what happens to a stack when its host reboots. A stack
 * without a desired state (never started, or last started before the agent
 * recorded one) stays stopped, like one that was stopped.
 */
export function rebootLabel(stack: Pick<Stack, "desired">): string {
  return stack.desired === "up" ? "Starts" : "Stays stopped";
}
export type StackOperation = { name: string; operation: string; success: boolean; output?: string };

/** A save's answer: the stack, and the `up` it ran when it changed a running stack's environment. */
export type StackSaved = Stack & { operation?: StackOperation };

/** The agent's limit on a stack's environment entries. */
export const maxEnvironment = 128;
const environmentKey = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * Reads the editor's environment field: KEY=VALUE lines, blank lines and
 * `#` comments skipped. A value is everything after the first "=", as is.
 * The same rules as the CLI's --env-file.
 */
export function parseEnvironment(text: string): Record<string, string> {
  const environment: Record<string, string> = {};
  text.split("\n").forEach((raw, index) => {
    const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) return;
    const at = line.indexOf("=");
    const where = `Environment line ${index + 1}`;
    if (at < 0) throw new Error(`${where}: expected KEY=VALUE.`);
    const key = line.slice(0, at);
    if (!environmentKey.test(key)) throw new Error(`${where}: "${key}" is not a valid variable name.`);
    if (Object.hasOwn(environment, key)) throw new Error(`${where}: ${key} is set twice.`);
    const value = line.slice(at + 1);
    if (/[\r\0]/.test(value)) throw new Error(`${where}: ${key} contains a carriage return or NUL.`);
    environment[key] = value;
  });
  if (Object.keys(environment).length > maxEnvironment)
    throw new Error(`A stack takes at most ${maxEnvironment} environment entries.`);
  return environment;
}

/** The editor's text for an environment: KEY=VALUE lines sorted by key. */
export function formatEnvironment(environment: Record<string, string> | undefined): string {
  return Object.keys(environment ?? {})
    .sort()
    .map((key) => `${key}=${environment?.[key]}`)
    .join("\n");
}

export const stackNamePattern = /^[a-z0-9][a-z0-9_-]{0,62}$/;

// A 401 means the session ended; the dashboard handles that apart from
// other failures. Other errors carry the master's message when it has one.
async function failure(response: Response, fallback: string): Promise<Error> {
  const text = (await response.text()).trim();
  if (response.status === 401) return new UnauthorizedError(text || fallback);
  return new Error(text || `${fallback} (${response.status}).`);
}

function endpoint(hostID: string, name?: string): string {
  const base = `/api/hosts/${encodeURIComponent(hostID)}/stacks`;
  return name === undefined ? base : `${base}/${encodeURIComponent(name)}`;
}

export async function listStacks(hostID: string, request: typeof fetch = fetch): Promise<Stack[]> {
  const response = await request(endpoint(hostID));
  if (!response.ok) throw await failure(response, "Could not load stacks");
  return (await response.json()) as Stack[];
}

export async function getStack(hostID: string, name: string, request: typeof fetch = fetch): Promise<Stack> {
  const response = await request(endpoint(hostID, name));
  if (!response.ok) throw await failure(response, "Could not load stack");
  return (await response.json()) as Stack;
}

/** One saved revision of a stack; revisions are listed by number only. */
export type StackVersion = { version: number };

export async function listStackVersions(
  hostID: string,
  name: string,
  request: typeof fetch = fetch,
): Promise<StackVersion[]> {
  const response = await request(`${endpoint(hostID, name)}/versions`);
  if (!response.ok) throw await failure(response, "Could not load revisions");
  return (await response.json()) as StackVersion[];
}

/** A revision's Compose document and environment. Reading changes nothing. */
export async function getStackRevision(
  hostID: string,
  name: string,
  version: number,
  request: typeof fetch = fetch,
): Promise<Stack> {
  const response = await request(`${endpoint(hostID, name)}/versions/${version}`);
  if (!response.ok) throw await failure(response, "Could not load revision");
  return (await response.json()) as Stack;
}

async function saveStack(
  hostID: string,
  name: string,
  composeYAML: string,
  expectedVersion: number | undefined,
  environment: Record<string, string> | undefined,
  request: typeof fetch,
): Promise<StackSaved> {
  if (!stackNamePattern.test(name)) throw new Error("Invalid stack name.");
  if (!composeYAML.trim()) throw new Error("Enter a Compose YAML document.");
  const response = await request(endpoint(hostID), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      name,
      compose_yaml: composeYAML,
      ...(expectedVersion === undefined ? {} : { expected_version: expectedVersion }),
      // Absent keeps the stack's current environment.
      ...(environment === undefined ? {} : { environment }),
    }),
  });
  if (!response.ok) throw await failure(response, "Save failed");
  return (await response.json()) as StackSaved;
}

export function createStack(
  hostID: string,
  name: string,
  composeYAML: string,
  environment: Record<string, string> | undefined,
  request: typeof fetch = fetch,
) {
  return saveStack(hostID, name, composeYAML, undefined, environment, request);
}

/**
 * Saves a new revision. An undefined environment keeps the current one; a
 * changed environment brings a running stack up again (see `operation`).
 */
export function updateStack(
  hostID: string,
  name: string,
  composeYAML: string,
  expectedVersion: number,
  environment: Record<string, string> | undefined,
  request: typeof fetch = fetch,
) {
  if (!Number.isSafeInteger(expectedVersion) || expectedVersion < 1) throw new Error("Invalid stack version.");
  return saveStack(hostID, name, composeYAML, expectedVersion, environment, request);
}

// Terminal control sequences: CSI (colours, cursor moves) and OSC (titles,
// links). Podman Compose prints them even when its output is not a terminal.
const csi = /\x1b\[[0-?]*[ -/]*[@-~]/g;
const osc = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g;

/** Command output as plain text: terminal control sequences removed. */
export function terminalText(output: string): string {
  return output.replace(osc, "").replace(csi, "");
}

/** A start or stop that ran and failed; `output` is what Podman Compose printed. */
export class StackOperationError extends Error {
  readonly output: string;
  constructor(message: string, output: string) {
    super(message);
    this.output = output;
  }
}

async function stackOperation(
  hostID: string,
  name: string,
  operation: "up" | "down",
  failed: string,
  request: typeof fetch,
): Promise<StackOperation> {
  const response = await request(`${endpoint(hostID, name)}/${operation}`, { method: "POST" });
  if (!response.ok) throw await failure(response, failed);
  const result = (await response.json()) as StackOperation;
  const output = terminalText(result.output ?? "");
  if (!result.success) throw new StackOperationError(`${failed}.`, output);
  return { ...result, output };
}

/** Starts the stack; the agent also starts it again after a reboot. */
export function deployStack(hostID: string, name: string, request: typeof fetch = fetch): Promise<StackOperation> {
  return stackOperation(hostID, name, "up", "Start failed", request);
}

/** Stops and removes the stack's containers; it stays stopped after a reboot. */
export function stopStack(hostID: string, name: string, request: typeof fetch = fetch): Promise<StackOperation> {
  return stackOperation(hostID, name, "down", "Stop failed", request);
}
