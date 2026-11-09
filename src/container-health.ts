// What a container's health check and restarts say, for its row and its stack.

/** How far back the agent reads Podman's events (protocol.RecentWindow). */
export const RECENT_WINDOW_MINUTES = 15;

/** A container's health and restart facts, as the agent reports them. */
export type HealthFacts = {
  /** Podman's container state ("running", "exited", ...). */
  state: string;
  /** The health check's result ("healthy", "unhealthy", "starting", ...); absent without a health check. */
  health?: string;
  /** Restarts by the restart policy since a user last started the container. */
  restarts?: number;
  /** Restarts by the restart policy within the recent window. */
  recent_restarts?: number;
  /** Seconds since the latest of those restarts. */
  last_restart_ago_s?: number;
  /** Seconds since the health check last reported unhealthy within the window. */
  unhealthy_ago_s?: number;
};

/**
 * Why a container needs attention, most urgent first: its health check fails
 * now; the restart policy restarted it within the window; or its health check
 * failed within the window and passes again. Health only counts while the
 * container runs; recent restarts count in any state, since a crash-looping
 * container is often caught between runs.
 */
export type Attention = "unhealthy" | "restarted" | "recovered";

export function attention(f: HealthFacts): Attention | undefined {
  const running = f.state === "running";
  if (running && f.health === "unhealthy") return "unhealthy";
  if ((f.recent_restarts ?? 0) > 0) return "restarted";
  if (running && f.health === "healthy" && (f.unhealthy_ago_s ?? 0) > 0) return "recovered";
  return undefined;
}

/** "just now" under a minute, else whole minutes ("4 min ago"). */
export function formatAgo(seconds: number): string {
  if (seconds < 60) return "just now";
  return `${Math.floor(seconds / 60)} min ago`;
}

/** One note beside a container's state; warn notes use the warning chip. */
export type HealthNoteItem = { text: string; warn: boolean; title?: string; className?: string };

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The notes for a container's row: its health, then its restarts. */
export function healthNotes(f: HealthFacts): HealthNoteItem[] {
  const notes: HealthNoteItem[] = [];
  if (f.state === "running" && f.health) {
    const unhealthyAgo = f.unhealthy_ago_s ?? 0;
    if (f.health === "unhealthy") notes.push({ text: "Unhealthy", warn: true });
    else if (f.health === "healthy" && unhealthyAgo > 0)
      notes.push({
        text: `Was unhealthy ${formatAgo(unhealthyAgo)}`,
        warn: true,
        title: `Healthy now; the health check last reported unhealthy ${formatAgo(unhealthyAgo)}`,
      });
    else notes.push({ text: capitalize(f.health), warn: false, className: `health-note ${f.health}` });
  }
  const recent = f.recent_restarts ?? 0;
  const total = f.restarts ?? 0;
  if (recent > 0) {
    const ago = formatAgo(f.last_restart_ago_s ?? 0);
    notes.push({
      text: recent === 1 ? `Restarted ${ago}` : `${recent} restarts in ${RECENT_WINDOW_MINUTES} min`,
      warn: true,
      title: `Last restart ${ago}; the restart policy restarted it ${total}× since it was started`,
    });
  } else if (total > 0) {
    notes.push({
      text: `Restarted ${total}×`,
      warn: false,
      className: "restart-note",
      title: "Restarts by the container's restart policy since it was started",
    });
  }
  return notes;
}
