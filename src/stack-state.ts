/** One container of a stack's service, as the agent reports it. */
export type StackService = {
  service: string;
  container?: string;
  image?: string;
  /** Podman's container state, or "absent", "unknown" from the agent. */
  state: string;
  status?: string;
  /** The health check's result ("healthy", "unhealthy", "starting", ...); absent without a health check. */
  health?: string;
  /** Restarts by the container's restart policy. */
  restarts?: number;
};

export type StackRunState = {
  label: string;
  /** Which chip style to show. */
  tone: "running" | "partial" | "degraded" | "stopped" | "unknown" | "invalid";
  /** Distinct services and how many have a running container. */
  total: number;
  running: number;
  /** Distinct services with a running container whose health check fails. */
  unhealthy: number;
};

/**
 * The stack's run state from its services. A service counts as running when
 * any of its containers runs, and as unhealthy when one of its running
 * containers fails its health check; a Compose file that does not parse is
 * "invalid". A stack whose services all run but some are unhealthy is
 * "degraded".
 */
export function stackRunState(stack: { status: string; services?: StackService[] }): StackRunState {
  if (stack.status === "invalid")
    return { label: "Compose file does not parse", tone: "invalid", total: 0, running: 0, unhealthy: 0 };
  const services = stack.services ?? [];
  if (services.some((s) => s.state === "unknown"))
    return { label: "Unknown", tone: "unknown", total: 0, running: 0, unhealthy: 0 };
  const declared = new Set(services.map((s) => s.service));
  const up = services.filter((s) => s.state === "running");
  const running = new Set(up.map((s) => s.service)).size;
  const unhealthy = new Set(up.filter((s) => s.health === "unhealthy").map((s) => s.service)).size;
  const total = declared.size;
  const counts = { total, running, unhealthy };
  const sick = unhealthy > 0 ? `, ${unhealthy} unhealthy` : "";
  if (total === 0) return { label: "No services", tone: "stopped", ...counts };
  if (running === total)
    return unhealthy > 0
      ? { label: `Running${sick}`, tone: "degraded", ...counts }
      : { label: "Running", tone: "running", ...counts };
  if (running === 0) return { label: "Stopped", tone: "stopped", ...counts };
  return { label: `${running} of ${total} running${sick}`, tone: "partial", ...counts };
}

/**
 * The label for one service's state. Podman's own states ("exited",
 * "created", "paused", ...) are capitalized; the agent's "absent" reads as
 * "Not created".
 */
export function serviceStateLabel(state: string): string {
  switch (state) {
    case "absent":
      return "Not created";
    case "":
      return "Unknown";
    default:
      return state.charAt(0).toUpperCase() + state.slice(1);
  }
}
