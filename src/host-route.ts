export type HostSection = "containers" | "images" | "volumes" | "stacks";
export type HostRoute = {
  hostID: string;
  section: HostSection;
  action?: "new" | "edit" | "view";
  stackName?: string;
};

const sections: readonly string[] = ["containers", "images", "volumes", "stacks"];

/** The fleet-wide enrollment page. Host routes always have a section after the host ID, so it cannot clash with one. */
export const enrollmentPath = "/enrollment";

export function hostPath(hostID: string, section: HostSection): string {
  return `/${encodeURIComponent(hostID)}/${section}`;
}

export function newStackPath(hostID: string): string {
  return `${hostPath(hostID, "stacks")}/new`;
}

export function stackPath(hostID: string, name: string): string {
  return `${hostPath(hostID, "stacks")}/${encodeURIComponent(name)}`;
}

export function editStackPath(hostID: string, name: string): string {
  return `${stackPath(hostID, name)}/edit`;
}

export function parseHostPath(pathname: string): HostRoute | null {
  const parts = pathname.split("/");
  if (parts[0] !== "" || !sections.includes(parts[2])) return null;
  const isNew = parts.length === 4 && parts[2] === "stacks" && parts[3] === "new";
  const isEdit = parts.length === 5 && parts[2] === "stacks" && parts[4] === "edit";
  // A stack's own page. "new" is the create page, so a stack cannot take that name.
  const isView = parts.length === 4 && parts[2] === "stacks" && !isNew;
  if (parts.length !== 3 && !isNew && !isEdit && !isView) return null;
  try {
    const hostID = decodeURIComponent(parts[1]);
    if (!hostID || hostID.includes("/") || hostID === "." || hostID === "..") return null;
    if (isEdit || isView) {
      const stackName = decodeURIComponent(parts[3]);
      if (!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(stackName)) return null;
      return { hostID, section: "stacks", action: isEdit ? "edit" : "view", stackName };
    }
    return { hostID, section: parts[2] as HostSection, ...(isNew ? { action: "new" as const } : {}) };
  } catch {
    return null;
  }
}
