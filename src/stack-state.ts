import { type Attention, type HealthFacts, attention } from "./container-health.ts";

/** One container of a stack's service, as the agent reports it. */
export type StackService = HealthFacts & {
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
  tone: "running" | "partial" | "degraded" | "stopped" | "unknown" | "invalid";
  /** Distinct services and how many have a running container. */
  total: number;
  running: number;
  /** Distinct services needing attention, each counted under its most urgent reason. */
  unhealthy: number;
  restarted: number;
  recovered: number;
};

const urgency: Attention[] = ["unhealthy", "restarted", "recovered"];

/**
 * The stack's run state from its services. A service counts as running when
 * any of its containers runs, and needs attention under the most urgent
 * reason any of its containers has (see attention); a Compose file that does
 * not parse is "invalid". A stack whose services all run but some need
 * attention is "degraded".
 */
export function stackRunState(stack: { status: string; services?: StackService[] }): StackRunState {
  const none = { total: 0, running: 0, unhealthy: 0, restarted: 0, recovered: 0 };
  if (stack.status === "invalid") return { label: "Compose file does not parse", tone: "invalid", ...none };
  const services = stack.services ?? [];
  if (services.some((s) => s.state === "unknown")) return { label: "Unknown", tone: "unknown", ...none };
  const declared = new Set(services.map((s) => s.service));
  const running = new Set(services.filter((s) => s.state === "running").map((s) => s.service)).size;
  const worst = new Map<string, number>();
  for (const s of services) {
    const reason = attention(s);
    if (reason === undefined) continue;
    const rank = urgency.indexOf(reason);
    const current = worst.get(s.service);
    if (current === undefined || rank < current) worst.set(s.service, rank);
  }
  const count = (reason: Attention) => [...worst.values()].filter((rank) => rank === urgency.indexOf(reason)).length;
  const total = declared.size;
  const counts = {
    total,
    running,
    unhealthy: count("unhealthy"),
    restarted: count("restarted"),
    recovered: count("recovered"),
  };
  const notes = urgency
    .filter((reason) => counts[reason] > 0)
    .map((reason) => `, ${counts[reason]} ${reason}`)
    .join("");
  if (total === 0) return { label: "No services", tone: "stopped", ...counts };
  if (running === total)
    return worst.size > 0
      ? { label: `Running${notes}`, tone: "degraded", ...counts }
      : { label: "Running", tone: "running", ...counts };
  if (running === 0 && worst.size === 0) return { label: "Stopped", tone: "stopped", ...counts };
  if (running === 0) return { label: `Stopped${notes}`, tone: "partial", ...counts };
  return { label: `${running} of ${total} running${notes}`, tone: "partial", ...counts };
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
