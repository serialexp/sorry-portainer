import { A } from "@solidjs/router";
import { For, type JSX, Show } from "solid-js";
import { hostPath } from "./host-route";

/** One step of a breadcrumb; the last one, the page itself, has no link. */
export type Crumb = { label: string; href?: string };

/** The crumbs every page inside a host starts with: the hosts, then the host's containers. */
export function hostCrumbs(hostID: string): Crumb[] {
  return [
    { label: "Hosts", href: "/" },
    { label: hostID, href: hostPath(hostID, "containers") },
  ];
}

/**
 * A page's header, as the design draws every host page: the breadcrumb, the
 * title with its status chip beside it (badge), what goes under the title
 * (chips or a sentence), and the page's actions on the right.
 */
export function PageHeader(props: {
  crumbs: Crumb[];
  title: JSX.Element;
  badge?: JSX.Element;
  children?: JSX.Element;
  actions?: JSX.Element;
}) {
  return (
    <header>
      <div>
        <nav class="breadcrumb" aria-label="Breadcrumb">
          <For each={props.crumbs}>
            {(crumb, index) => (
              <>
                <Show when={index() > 0}>
                  <span aria-hidden="true">/</span>
                </Show>
                <Show when={crumb.href} fallback={<span aria-current="page">{crumb.label}</span>}>
                  {(href) => <A href={href()}>{crumb.label}</A>}
                </Show>
              </>
            )}
          </For>
        </nav>
        <div class="page-title">
          <h1>{props.title}</h1>
          {props.badge}
        </div>
        {props.children}
      </div>
      <Show when={props.actions}>
        <div class="header-actions">{props.actions}</div>
      </Show>
    </header>
  );
}
