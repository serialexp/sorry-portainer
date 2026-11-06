/** One container of a stack's service, as the agent reports it. */
export type StackService = {
  service: string;
  container?: string;
  image?: string;
  /** Podman's container state, or "absent", "unknown" from the agent. */
  state: string;
  status?: string;
};

export type StackRunState = {
  label: string;
  /** Which chip style to show. */
  tone: "running" | "partial" | "stopped" | "unknown" | "invalid";
  /** Distinct services and how many have a running container. */
  total: number;
  running: number;
};

/**
 * The stack's run state from its services. A service counts as running when
 * any of its containers runs; a Compose file that does not parse is "invalid".
 */
export function stackRunState(stack: { status: string; services?: StackService[] }): StackRunState {
  if (stack.status === "invalid")
    return { label: "Compose file does not parse", tone: "invalid", total: 0, running: 0 };
  const services = stack.services ?? [];
  if (services.some((s) => s.state === "unknown")) return { label: "Unknown", tone: "unknown", total: 0, running: 0 };
  const declared = new Set(services.map((s) => s.service));
  const running = new Set(services.filter((s) => s.state === "running").map((s) => s.service)).size;
  const total = declared.size;
  if (total === 0) return { label: "No services", tone: "stopped", total, running };
  if (running === total) return { label: "Running", tone: "running", total, running };
  if (running === 0) return { label: "Stopped", tone: "stopped", total, running };
  return { label: `${running} of ${total} running`, tone: "partial", total, running };
}
