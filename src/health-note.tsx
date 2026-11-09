import { For } from "solid-js";
import { type HealthFacts, healthNotes } from "./container-health";

/**
 * A container's health check result and its restarts by the restart policy,
 * beside its state. Nothing shows for a container without a health check that
 * never restarted.
 */
export function HealthNote(props: { facts: HealthFacts }) {
  return (
    <For each={healthNotes(props.facts)}>
      {(note) => (
        <span class={note.warn ? "warn-chip" : note.className} title={note.title}>
          {note.text}
        </span>
      )}
    </For>
  );
}
