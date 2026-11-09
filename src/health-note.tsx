import { Show } from "solid-js";

/**
 * A running container's health check result and its restarts by the restart
 * policy, beside its state. Nothing shows for a container without a health
 * check that never restarted.
 */
export function HealthNote(props: { state: string; health?: string; restarts?: number }) {
  const health = () => (props.state === "running" ? props.health : undefined);
  const restarts = () => props.restarts ?? 0;
  return (
    <>
      <Show when={health()}>
        {(h) => (
          <span class={h() === "unhealthy" ? "warn-chip" : `health-note ${h()}`}>
            {h() === "unhealthy" ? "Unhealthy" : h().charAt(0).toUpperCase() + h().slice(1)}
          </span>
        )}
      </Show>
      <Show when={restarts() > 0}>
        <span class="restart-note" title="Restarts by the container's restart policy">
          Restarted {restarts()}×
        </span>
      </Show>
    </>
  );
}
