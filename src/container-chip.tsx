import { A } from "@solidjs/router";
import { Show } from "solid-js";

/**
 * A container of this agent that uses a volume or an image: a state dot, a
 * link to the host's containers and, when there is one, what it does with it.
 */
export function ContainerChip(props: {
  href: string;
  container: string;
  state: string;
  detail?: string;
  title?: string;
}) {
  return (
    <span class="container-chip" title={props.title ?? props.container}>
      <span class={`container-dot ${props.state === "running" ? "up" : "down"}`} aria-hidden="true" />
      <A href={props.href}>{props.container}</A>
      <Show when={props.detail}>{(detail) => <span class="container-detail">→ {detail()}</span>}</Show>
    </span>
  );
}
