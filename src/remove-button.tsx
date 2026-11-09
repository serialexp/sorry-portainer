import { createSignal, Show } from "solid-js";
import { sessionEnded } from "./fleet-store";
import { TrashIcon } from "./icons";

/**
 * A remove action that asks first, as the stack pages do, and keeps the host's
 * reason on screen when the host refuses the removal.
 */
export function RemoveButton(props: {
  label: string;
  question: string;
  /** Why the removal is not offered, such as a volume that a container mounts. */
  blocked?: string;
  /** The button's text while blocked, when it differs from the label, such as "In use elsewhere". */
  blockedLabel?: string;
  /** "header" draws it as a page header's action: a plain button with a trash icon. */
  variant?: "header";
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
        class={props.variant === "header" ? "add-button" : "remove-button"}
        type="button"
        disabled={busy() || Boolean(props.blocked)}
        title={props.blocked}
        onClick={() => void remove()}
      >
        <Show when={props.variant === "header"}>
          <TrashIcon />
        </Show>
        {busy() ? "Removing…" : props.blocked && props.blockedLabel ? props.blockedLabel : props.label}
      </button>
      <Show when={error()}>
        <p class="form-error" role="alert">
          {error()}
        </p>
      </Show>
    </div>
  );
}
