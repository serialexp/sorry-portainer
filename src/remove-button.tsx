import { createSignal, Show } from "solid-js";
import { sessionEnded } from "./fleet-store";

/**
 * A remove action that asks first, as the stack pages do, and keeps the host's
 * reason on screen when the host refuses the removal.
 */
export function RemoveButton(props: {
  label: string;
  question: string;
  /** Why the removal is not offered, such as a volume that a container mounts. */
  blocked?: string;
  onRemove: () => Promise<void>;
}) {
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");

  async function remove() {
    if (props.blocked || busy() || !window.confirm(props.question)) return;
    setBusy(true);
    setError("");
    try {
      await props.onRemove();
    } catch (cause) {
      if (!sessionEnded(cause)) setError(cause instanceof Error ? cause.message : "Could not remove it.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div class="remove-control">
      <button
        class="remove-button"
        type="button"
        disabled={busy() || Boolean(props.blocked)}
        title={props.blocked}
        onClick={() => void remove()}
      >
        {busy() ? "Removing…" : props.label}
      </button>
      <Show when={error()}>
        <p class="form-error" role="alert">
          {error()}
        </p>
      </Show>
    </div>
  );
}
