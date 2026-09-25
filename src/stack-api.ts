export type Stack = {
  name: string;
  project: string;
  status: string;
  version: number;
  compose_yaml?: string;
  /** Stack secrets the Compose file uses (single-stack reads only). */
  secrets?: string[];
};
export type StackOperation = { name: string; operation: string; success: boolean; output?: string };

export const stackNamePattern = /^[a-z0-9][a-z0-9_-]{0,62}$/;

function endpoint(hostID: string, name?: string): string {
  const base = `/api/hosts/${encodeURIComponent(hostID)}/stacks`;
  return name === undefined ? base : `${base}/${encodeURIComponent(name)}`;
}

export async function listStacks(hostID: string, request: typeof fetch = fetch): Promise<Stack[]> {
  const response = await request(endpoint(hostID));
  if (!response.ok) throw new Error(`Could not load stacks (${response.status}).`);
  return (await response.json()) as Stack[];
}

export async function getStack(hostID: string, name: string, request: typeof fetch = fetch): Promise<Stack> {
  const response = await request(endpoint(hostID, name));
  if (!response.ok) throw new Error(`Could not load stack (${response.status}).`);
  return (await response.json()) as Stack;
}

async function saveStack(
  hostID: string,
  name: string,
  composeYAML: string,
  expectedVersion: number | undefined,
  request: typeof fetch,
) {
  if (!stackNamePattern.test(name)) throw new Error("Invalid stack name.");
  if (!composeYAML.trim()) throw new Error("Enter a Compose YAML document.");
  const response = await request(endpoint(hostID), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      name,
      compose_yaml: composeYAML,
      ...(expectedVersion === undefined ? {} : { expected_version: expectedVersion }),
    }),
  });
  if (!response.ok) throw new Error((await response.text()).trim() || `Save failed (${response.status}).`);
}

export function createStack(hostID: string, name: string, composeYAML: string, request: typeof fetch = fetch) {
  return saveStack(hostID, name, composeYAML, undefined, request);
}

export function updateStack(
  hostID: string,
  name: string,
  composeYAML: string,
  expectedVersion: number,
  request: typeof fetch = fetch,
) {
  if (!Number.isSafeInteger(expectedVersion) || expectedVersion < 1) throw new Error("Invalid stack version.");
  return saveStack(hostID, name, composeYAML, expectedVersion, request);
}

export async function deployStack(
  hostID: string,
  name: string,
  request: typeof fetch = fetch,
): Promise<StackOperation> {
  const response = await request(`${endpoint(hostID, name)}/up`, { method: "POST" });
  if (!response.ok) throw new Error((await response.text()).trim() || `Deploy failed (${response.status}).`);
  const result = (await response.json()) as StackOperation;
  if (!result.success) throw new Error(result.output || "Deploy failed.");
  return result;
}
