import { A, useLocation, useNavigate } from "@solidjs/router";
import { createEffect, createSignal, For, Match, on, onCleanup, onMount, Show, Switch, untrack } from "solid-js";
import { filteredStacks, fleet, loadStacks, searchTerm, sessionEnded } from "./fleet-store";
import { editStackPath, hostPath, newStackPath, parseHostPath, stackPath } from "./host-route";
import { HealthNote } from "./health-note";
import {
  ArrowUpIcon,
  ChevronRightIcon,
  InfoIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  RefreshIcon,
  StackIcon,
  StopIcon,
} from "./icons";
import {
  askUpdate,
  cancelUpdate,
  checkImages,
  confirmUpdate,
  hideUpdateResult,
  imageUpdates,
  serviceImage,
  showImagesOf,
  tick,
} from "./image-update-store";
import {
  checkedText,
  imageStatusLine,
  updateStepText,
  registryChecked,
  updateDetail,
  updatePrompt,
  updatesLabel,
} from "./image-updates";
import { hostCrumbs, PageHeader } from "./page-header";
import { NoSecretStore, StackSecretsPanel, StackStoreNotice } from "./secrets-ui";
import {
  type StackRunState,
  type StackService,
  serviceStateLabel,
  rowActions,
  serviceSummary,
  stackPill,
  stackRunState,
} from "./stack-state";
import { secrets, setReferencedSecrets } from "./secrets-store";
import {
  createStack,
  deployStack,
  formatEnvironment,
  getStack,
  getStackRevision,
  listStackVersions,
  parseEnvironment,
  rebootLabel,
  StackOperationError,
  stackNamePattern,
  stopStack,
  terminalText,
  updateStack,
  type Stack,
  type StackOperation,
} from "./stack-api";

/**
 * A stack's services with whether their images are still what their tags
 * point to: the updates count, when the images were checked, and Check now.
 */
function ServicesSection(props: { name: string; services: StackService[] }) {
  const count = () => new Set(props.services.map((s) => s.service)).size;
  const checkLine = () => {
    if (imageUpdates.checking) return "Checking images…";
    if (imageUpdates.error) return `Couldn’t check images: ${imageUpdates.error}`;
    return checkedText(imageUpdates.services, imageUpdates.now);
  };
  return (
    <section class="stack-services-section" aria-labelledby="stack-services-heading">
      <div class="stack-services-head">
        <div class="stack-services-title">
          <h3 id="stack-services-heading">Services</h3>
          <span class="resource-count">{count()}</span>
        </div>
        <div class="stack-services-tools">
          <Show when={updatesLabel(imageUpdates.services)}>
            {(label) => <span class="warn-chip dotted">{label()}</span>}
          </Show>
          <span class="stack-services-checked" role="status">
            {checkLine()}
          </span>
          <button
            class="small-button"
            classList={{ checking: imageUpdates.checking }}
            type="button"
            disabled={imageUpdates.checking || imageUpdates.loading || !!imageUpdates.updating}
            onClick={() => checkImages(true)}
          >
            <RefreshIcon size={13} />
            Check now
          </button>
        </div>
      </div>
      <ServiceTable name={props.name} services={props.services} />
    </section>
  );
}

/** A stack's services, one row per container; a service's image state and Update show on its first row. */
function ServiceTable(props: { name: string; services: StackService[] }) {
  const stackRuns = () => props.services.some((s) => s.state === "running");
  const firstOfService = (index: number) =>
    props.services.findIndex((s) => s.service === props.services[index].service) === index;
  return (
    <div class="stack-services" role="table" aria-label={`Services of ${props.name}`}>
      <div class="stack-service-row stack-service-head" role="row">
        <span role="columnheader">Service</span>
        <span role="columnheader">Container</span>
        <span role="columnheader">Image</span>
        <span role="columnheader">State</span>
        <span role="columnheader" class="stack-service-update">
          Image
        </span>
      </div>
      <For each={props.services}>
        {(svc, index) => <ServiceRow service={svc} first={firstOfService(index())} stackRuns={stackRuns()} />}
      </For>
    </div>
  );
}

/** Under a service's image: whether it runs what its tag points to now. */
function ImageStatus(props: { service: string }) {
  const line = () => {
    const img = serviceImage(props.service);
    if (imageUpdates.updating === props.service)
      return { text: updateStepText(imageUpdates.step), tone: "warn busy", title: "" };
    if (imageUpdates.checking && img && registryChecked(img.status))
      return { text: "Checking…", tone: "muted busy", title: "" };
    if (!img) return imageUpdates.loading ? { text: "Checking…", tone: "muted busy", title: "" } : undefined;
    return imageStatusLine(img, imageUpdates.now);
  };
  return (
    <Show when={line()}>
      {(l) => (
        <span class={`image-status ${l().tone}`} title={l().title || undefined}>
          {l().text}
        </span>
      )}
    </Show>
  );
}

/** The row under a service that confirms its update. */
function UpdateConfirm(props: { service: string; stackRuns: boolean }) {
  const img = () => serviceImage(props.service);
  const prompt = () => updatePrompt(props.service, img()?.image ?? "", props.stackRuns);
  return (
    <div class="stack-service-confirm" role="row">
      <div role="cell" class="update-confirm" aria-label={`Update ${props.service}`}>
        <div class="update-confirm-text">
          <span>{prompt().text}</span>
          <Show when={img()}>
            {(i) => <span class="update-confirm-detail">{updateDetail(i(), imageUpdates.now)}</span>}
          </Show>
        </div>
        <div class="update-confirm-actions">
          <button class="add-button" type="button" onClick={() => cancelUpdate()}>
            Cancel
          </button>
          <button class="add-button solid" type="button" onClick={() => confirmUpdate(props.service, props.stackRuns)}>
            {prompt().label}
          </button>
        </div>
      </div>
    </div>
  );
}

/** The title chip for a stack's run state: green running, amber partly up or with problems, red broken, grey otherwise. */
function titleChipClass(tone: StackRunState["tone"]): string {
  switch (tone) {
    case "running":
      return "title-chip";
    case "partial":
    case "degraded":
      return "title-chip warn";
    case "invalid":
      return "title-chip danger";
    default:
      return "title-chip stopped";
  }
}

/** One service row of a stack card: its container, image and state; on a service's first row, its image state and Update. */
function ServiceRow(props: { service: StackService; first: boolean; stackRuns: boolean }) {
  const canUpdate = () =>
    props.first &&
    serviceImage(props.service.service)?.status === "behind" &&
    imageUpdates.confirm !== props.service.service &&
    !imageUpdates.updating &&
    !imageUpdates.checking;
  const updateLabel = () =>
    `Update ${props.service.service} to the newest ${serviceImage(props.service.service)?.image ?? "image"}`;
  return (
    <>
      <div class="stack-service-row" role="row">
        <span class="stack-service-name" role="cell">
          {props.service.service}
        </span>
        <span class="cell-muted" role="cell">
          {props.service.container || "—"}
        </span>
        <span class="stack-service-image" role="cell">
          <span class="chip-code" title={props.service.image || undefined}>
            {props.service.image || "—"}
          </span>
          <Show when={props.first}>
            <ImageStatus service={props.service.service} />
          </Show>
        </span>
        <span role="cell" class="stack-service-state">
          <span class="state-line">
            <span class={props.service.state === "running" ? "state" : "state stopped"}>
              {serviceStateLabel(props.service.state)}
            </span>
            <HealthNote facts={props.service} />
          </span>
          <Show when={props.service.status}>
            <small class="stack-service-status">{props.service.status}</small>
          </Show>
        </span>
        <span role="cell" class="stack-service-update">
          <Show when={canUpdate()}>
            <button
              class="add-button solid"
              type="button"
              aria-label={updateLabel()}
              onClick={() => askUpdate(props.service.service)}
            >
              <ArrowUpIcon />
              Update
            </button>
          </Show>
        </span>
      </div>
      <Show when={props.first && imageUpdates.confirm === props.service.service}>
        <UpdateConfirm service={props.service.service} stackRuns={props.stackRuns} />
      </Show>
    </>
  );
}

/** What the last start or stop did: its stack, a title ("Started web", or the failure) and Podman Compose's output. */
export type StackRunResult = { name: string; failed: boolean; title: string; output: string };

/** Start and stop for a host's stacks, with the busy state and the last result a page shows. */
export function createStackRunner() {
  const [busy, setBusy] = createSignal("");
  const [result, setResult] = createSignal<StackRunResult | null>(null);
  const [busyOperation, setBusyOperation] = createSignal<"up" | "down">("up");
  async function run(name: string, operation: "up" | "down") {
    const verb = operation === "up" ? "start" : "stop";
    const question = `Stop ${name}? Its containers are removed (volumes are kept) and it stays stopped after a reboot.`;
    if (operation === "down" && !window.confirm(question)) return;
    setBusy(name);
    setBusyOperation(operation);
    setResult(null);
    const hostID = fleet.selectedHost;
    try {
      const done = operation === "up" ? await deployStack(hostID, name) : await stopStack(hostID, name);
      if (fleet.selectedHost !== hostID) return;
      const title = `${operation === "up" ? "Started" : "Stopped"} ${name}`;
      setResult({ name, failed: false, title, output: done.output ?? "" });
    } catch (cause) {
      if (fleet.selectedHost === hostID && !sessionEnded(cause))
        setResult({
          name,
          failed: true,
          title: cause instanceof Error ? cause.message : `Could not ${verb} stack.`,
          output: cause instanceof StackOperationError ? cause.output : "",
        });
    } finally {
      setBusy("");
      // A failed start still changes what the stack comes back to after a reboot.
      if (fleet.selectedHost === hostID) void loadStacks();
    }
  }
  const busyLabel = (name: string, operation: "up" | "down", idle: string, active: string) =>
    busy() === name && busyOperation() === operation ? active : idle;
  const operationError = () => (result()?.failed ? result()?.title : "");
  const operationOutput = () => result()?.output ?? "";
  return { busy, result, hideResult: () => setResult(null), operationError, operationOutput, run, busyLabel };
}

/** A start or stop's result inside the row or page it belongs to: the title, then the output. */
function RunResult(props: { result: StackRunResult; onHide: () => void }) {
  return (
    <div class="run-result">
      <div class="run-result-head">
        <span
          class={props.result.failed ? "run-result-title failed" : "run-result-title"}
          role={props.result.failed ? "alert" : "status"}
        >
          {props.result.title}
        </span>
        <button class="small-button" type="button" onClick={() => props.onHide()}>
          Hide output
        </button>
      </div>
      <Show when={props.result.output}>
        <pre class="stack-output">{props.result.output}</pre>
      </Show>
    </div>
  );
}

/** The selected host's stacks, one row each. */
export function StackList() {
  const { busy, result, hideResult, run, busyLabel } = createStackRunner();
  return (
    <>
      <Show when={fleet.stacksError}>
        <p class="form-error" role="alert">
          {fleet.stacksError}
        </p>
      </Show>
      <Show
        when={!fleet.stacksLoading || fleet.stacks.length}
        fallback={<div class="resource-empty">Loading stacks…</div>}
      >
        <Switch
          fallback={
            <div class="empty-card">
              <span class="empty-icon" aria-hidden="true">
                <StackIcon size={24} />
              </span>
              <h3>No stacks saved on {fleet.selectedHost}</h3>
              <p>
                A stack is a Compose file saved here. Compose projects started outside sorry,portainer don’t appear in
                this list.
              </p>
              <A class="add-button solid" href={newStackPath(fleet.selectedHost)}>
                <PlusIcon />
                New stack
              </A>
            </div>
          }
        >
          <Match when={filteredStacks().length}>
            <section class="stack-rows" aria-label={`Stacks on ${fleet.selectedHost}`}>
              <For each={filteredStacks()}>
                {(stack) => {
                  const runState = () => stackRunState(stack);
                  const pill = () => stackPill(runState());
                  const offered = () => rowActions(runState());
                  const rowResult = () => (result()?.name === stack.name ? result() : null);
                  return (
                    <div class="stack-row">
                      <span class="stack-row-icon" aria-hidden="true">
                        <StackIcon size={20} />
                      </span>
                      <div class="stack-row-title">
                        <h2>
                          <A href={stackPath(fleet.selectedHost, stack.name)}>{stack.name}</A>
                        </h2>
                        <span>
                          {stack.project} · v{stack.version || 1}
                        </span>
                      </div>
                      <span
                        class={`stack-pill ${pill().tone}`}
                        title={
                          runState().tone === "invalid"
                            ? "Fix the Compose file to start this stack"
                            : serviceSummary(stack.services ?? [])
                        }
                      >
                        <span class="pill-dot" aria-hidden="true"></span>
                        {pill().text}
                      </span>
                      <div class="stack-row-actions">
                        <Show when={offered().start}>
                          <button
                            class="start"
                            type="button"
                            disabled={!!busy()}
                            aria-label={`Start ${stack.name}`}
                            onClick={() => run(stack.name, "up")}
                          >
                            <PlayIcon />
                            {busyLabel(stack.name, "up", "Start", "Starting…")}
                          </button>
                        </Show>
                        <Show when={offered().stop}>
                          <button
                            class="stop"
                            type="button"
                            disabled={!!busy()}
                            aria-label={`Stop ${stack.name}`}
                            onClick={() => run(stack.name, "down")}
                          >
                            <StopIcon />
                            {busyLabel(stack.name, "down", "Stop", "Stopping…")}
                          </button>
                        </Show>
                        <A
                          class="stack-row-open"
                          href={stackPath(fleet.selectedHost, stack.name)}
                          aria-label={`Open ${stack.name}`}
                        >
                          <ChevronRightIcon />
                        </A>
                      </div>
                      <Show when={runState().tone === "invalid"}>
                        <p class="stack-row-note invalid" role="alert">
                          <InfoIcon />
                          <span>The Compose file does not parse, so the stack cannot start.</span>
                          <A href={editStackPath(fleet.selectedHost, stack.name)}>Edit compose</A>
                        </p>
                      </Show>
                      <Show when={stack.waiting}>
                        <p class="stack-row-note" role="status">
                          <InfoIcon />
                          <span>{stack.waiting}</span>
                        </p>
                      </Show>
                      <Show when={rowResult()}>
                        {(current) => <RunResult result={current()} onHide={hideResult} />}
                      </Show>
                    </div>
                  );
                }}
              </For>
            </section>
          </Match>
          <Match when={searchTerm()}>
            <div class="no-matches">No stacks match that search.</div>
          </Match>
        </Switch>
      </Show>
    </>
  );
}

function sameEnvironment(a: Record<string, string>, b: Record<string, string>): boolean {
  return [...new Set([...Object.keys(a), ...Object.keys(b)])].every((key) => a[key] === b[key]);
}

/**
 * One stack's page: its state and services, the secrets it uses, and its
 * revisions, which can be viewed or restored. A restore saves the chosen
 * revision's contents as the next revision.
 */
export function StackDetail() {
  const location = useLocation();
  const hostID = () => fleet.selectedHost;
  const name = () => parseHostPath(location.pathname)?.stackName ?? "";
  const runner = createStackRunner();
  const [stack, setStack] = createSignal<Stack | null>(null);
  const [revisions, setRevisions] = createSignal<number[]>([]);
  const [loading, setLoading] = createSignal(false);
  const [loadError, setLoadError] = createSignal("");
  // The revision the page is showing, if any, and the revision being fetched or restored.
  const [shown, setShown] = createSignal<Stack | null>(null);
  const [revisionBusy, setRevisionBusy] = createSignal(0);
  const [actionError, setActionError] = createSignal("");
  const [actionOutput, setActionOutput] = createSignal("");
  // Answers for an earlier stack are dropped, as in the editor.
  let request = 0;

  async function load() {
    const host = hostID();
    const stackName = name();
    const id = ++request;
    setLoading(true);
    setLoadError("");
    try {
      const [current, versions] = await Promise.all([getStack(host, stackName), listStackVersions(host, stackName)]);
      if (id !== request) return;
      setStack(current);
      setRevisions(versions.map((v) => v.version).sort((a, b) => b - a));
    } catch (cause) {
      if (id === request && !sessionEnded(cause))
        setLoadError(cause instanceof Error ? cause.message : "Could not load stack.");
    } finally {
      if (id === request) setLoading(false);
    }
  }

  createEffect(() => {
    void hostID();
    void name();
    setStack(null);
    setShown(null);
    setActionError("");
    setActionOutput("");
    void load();
    const host = hostID();
    const stackName = name();
    // The check reads the image store; this effect follows only the stack shown.
    untrack(() => {
      showImagesOf(host, stackName);
      void checkImages();
    });
  });
  // After an update, show the recreated containers.
  createEffect(
    on(
      () => imageUpdates.completed,
      () => void load(),
      { defer: true },
    ),
  );
  // Keeps "checked X ago" current while the page is open.
  onMount(() => {
    const timer = setInterval(tick, 30_000);
    onCleanup(() => clearInterval(timer));
  });

  const runState = () => {
    const current = stack();
    return current ? stackRunState(current) : undefined;
  };
  // Start and Stop as the stack list offers them, once the stack has loaded.
  const actions = () => {
    const state = runState();
    return state ? rowActions(state) : { start: false, stop: false };
  };

  // Start or stop, then reload so the page shows what the stack is now.
  async function operate(operation: "up" | "down") {
    await runner.run(name(), operation);
    await load();
  }

  async function toggleView(version: number) {
    if (shown()?.version === version) {
      setShown(null);
      return;
    }
    setRevisionBusy(version);
    setActionError("");
    try {
      setShown(await getStackRevision(hostID(), name(), version));
    } catch (cause) {
      if (!sessionEnded(cause)) setActionError(cause instanceof Error ? cause.message : "Could not load revision.");
    } finally {
      setRevisionBusy(0);
    }
  }

  async function restore(version: number) {
    const current = stack();
    if (!current) return;
    setRevisionBusy(version);
    setActionError("");
    setActionOutput("");
    try {
      const revision = await getStackRevision(hostID(), name(), version);
      const environment = revision.environment ?? {};
      const environmentChanged = !sameEnvironment(environment, current.environment ?? {});
      const question =
        `Restore revision ${version}? It is saved as revision ${current.version + 1}, with that revision's Compose file and environment.` +
        (environmentChanged ? " The environment differs from the current one, so a running stack is restarted." : "");
      if (!window.confirm(question)) return;
      const saved = await updateStack(hostID(), name(), revision.compose_yaml ?? "", current.version, environment);
      setShown(null);
      if (saved.operation && !saved.operation.success) {
        setActionError(`Saved as revision ${saved.version}, but restarting the stack failed.`);
        setActionOutput(terminalText(saved.operation.output || ""));
      }
    } catch (cause) {
      if (!sessionEnded(cause)) setActionError(cause instanceof Error ? cause.message : "Could not restore revision.");
    } finally {
      setRevisionBusy(0);
      await load();
    }
  }

  return (
    <>
      <PageHeader
        crumbs={[...hostCrumbs(hostID()), { label: "Stacks", href: hostPath(hostID(), "stacks") }, { label: name() }]}
        title={name()}
        badge={
          <Show when={runState()}>{(state) => <span class={titleChipClass(state().tone)}>{state().label}</span>}</Show>
        }
        actions={
          <>
            <A class="add-button" href={editStackPath(hostID(), name())}>
              <PencilIcon />
              Edit compose
            </A>
            <Show when={actions().stop}>
              <button class="add-button" type="button" disabled={!!runner.busy()} onClick={() => operate("down")}>
                <StopIcon />
                {runner.busyLabel(name(), "down", "Stop stack", "Stopping…")}
              </button>
            </Show>
            <Show when={actions().start}>
              <button class="add-button solid" type="button" disabled={!!runner.busy()} onClick={() => operate("up")}>
                <PlayIcon />
                {runner.busyLabel(name(), "up", "Start stack", "Starting…")}
              </button>
            </Show>
          </>
        }
      >
        <Show when={stack()}>
          {(current) => (
            <dl class="host-chips">
              <div>
                <dt>Project</dt>
                <dd class="mono">{current().project}</dd>
              </div>
              <div>
                <dt>Revision</dt>
                <dd>v{current().version}</dd>
              </div>
              <div>
                <dt>After reboot</dt>
                <dd>{rebootLabel(current())}</dd>
              </div>
            </dl>
          )}
        </Show>
      </PageHeader>
      <StackStoreNotice />
      <section class="panel resource-page stack-detail">
        <div class="detail-notices">
          <Show when={loadError()}>
            <p class="form-error" role="alert">
              {loadError()}
            </p>
          </Show>
          <Show when={runner.result()}>{(result) => <RunResult result={result()} onHide={runner.hideResult} />}</Show>
          <Show when={imageUpdates.result}>
            {(result) => <RunResult result={{ name: name(), ...result() }} onHide={hideUpdateResult} />}
          </Show>
          <Show when={actionError()}>
            <p class="form-error" role="alert">
              {actionError()}
            </p>
          </Show>
          <Show when={actionOutput()}>
            <pre class="stack-output">{actionOutput()}</pre>
          </Show>
          <Show when={loading() && !stack()}>
            <p class="resource-empty">Loading stack…</p>
          </Show>
          <Show when={stack()?.status === "invalid"}>
            <p class="stack-row-note invalid" role="alert">
              <InfoIcon />
              <span>
                The Compose file does not parse, so the stack cannot start.{" "}
                <A href={editStackPath(hostID(), name())}>Edit compose</A>
              </span>
            </p>
          </Show>
          <Show when={stack()?.waiting}>
            <p class="stack-row-note" role="status">
              <InfoIcon />
              <span>{stack()?.waiting}</span>
            </p>
          </Show>
        </div>
        <Show when={stack()?.services?.length}>
          <ServicesSection name={name()} services={stack()?.services ?? []} />
        </Show>
        <div class="stack-detail-grid">
          <section class="stack-detail-block" aria-labelledby="stack-secrets-heading">
            <h3 id="stack-secrets-heading">Secrets</h3>
            <Show when={stack()?.secrets?.length} fallback={<p class="stack-note">This stack uses no secrets.</p>}>
              <ul class="stack-chip-list">
                <For each={stack()?.secrets}>{(secret) => <li class="chip-code">{secret}</li>}</For>
              </ul>
              <p class="stack-note">
                Their values are set on the <A href={editStackPath(hostID(), name())}>edit page</A>.
              </p>
            </Show>
          </section>
          <section class="stack-detail-block" aria-labelledby="stack-revisions-heading">
            <h3 id="stack-revisions-heading">Revisions</h3>
            <p class="stack-note">Every save keeps the previous Compose file. Restoring creates a new revision.</p>
            <ul class="revision-list">
              <For each={revisions()}>
                {(version) => (
                  <li class="revision-row">
                    <strong>v{version}</strong>
                    <Show when={stack()?.version === version}>
                      <span class="state">Current</span>
                    </Show>
                    <div class="stack-card-actions">
                      <button
                        class="add-button"
                        type="button"
                        disabled={revisionBusy() !== 0}
                        onClick={() => toggleView(version)}
                      >
                        {shown()?.version === version ? "Hide" : "View"}
                      </button>
                      <Show when={stack()?.version !== version}>
                        <button
                          class="add-button"
                          type="button"
                          disabled={revisionBusy() !== 0 || !stack()}
                          onClick={() => restore(version)}
                        >
                          {revisionBusy() === version ? "Restoring…" : "Restore"}
                        </button>
                      </Show>
                    </div>
                  </li>
                )}
              </For>
            </ul>
          </section>
        </div>
        <Show when={shown()}>
          {(revision) => (
            <section class="stack-detail-block" aria-label={`Revision ${revision().version}`}>
              <h3>Revision v{revision().version} · Compose file</h3>
              <pre class="stack-output">{revision().compose_yaml}</pre>
              <h3>Environment</h3>
              <pre class="stack-output">{formatEnvironment(revision().environment) || "No environment."}</pre>
            </section>
          )}
        </Show>
      </section>
    </>
  );
}

/** Creates a stack on the selected host, or edits the one the URL names. */
export function StackEditor() {
  const navigate = useNavigate();
  const location = useLocation();
  const currentHost = () => fleet.selectedHost;
  const editedStack = () => parseHostPath(location.pathname)?.stackName;
  const [name, setName] = createSignal("");
  const [compose, setCompose] = createSignal("");
  // KEY=VALUE lines. Sent only once edited, so a compose-only save leaves
  // the stack's environment (and its running containers) alone.
  const [environmentText, setEnvironmentText] = createSignal("");
  const [environmentEdited, setEnvironmentEdited] = createSignal(false);
  const [version, setVersion] = createSignal(0);
  const [error, setError] = createSignal("");
  const [loading, setLoading] = createSignal(false);
  const [saving, setSaving] = createSignal(false);
  const [deploying, setDeploying] = createSignal(false);
  const [saved, setSaved] = createSignal(false);
  const [deployOutput, setDeployOutput] = createSignal("");
  // The Compose file or environment changed since the stack was loaded or last saved.
  const [dirty, setDirty] = createSignal(false);
  // A stack this editor created; later saves update it, and its name is fixed.
  const [created, setCreated] = createSignal("");
  const existingStack = () => editedStack() ?? (created() || undefined);
  const busy = () => saving() || deploying() || loading();
  // Answers for an earlier route are dropped; returning to the same stack
  // starts a new request.
  let loadRequest = 0;
  createEffect(() => {
    const hostID = currentHost();
    const stackName = editedStack();
    const request = ++loadRequest;
    setName(stackName ?? "");
    setCreated("");
    setCompose("");
    setEnvironmentText("");
    setEnvironmentEdited(false);
    setVersion(0);
    setSaved(false);
    setDirty(false);
    setError("");
    setDeployOutput("");
    if (!stackName) {
      setLoading(false);
      return;
    }
    setLoading(true);
    void getStack(hostID, stackName)
      .then((stack) => {
        if (request !== loadRequest) return;
        setCompose(stack.compose_yaml ?? "");
        setEnvironmentText(formatEnvironment(stack.environment));
        setVersion(stack.version);
        setReferencedSecrets(hostID, stackName, stack.secrets ?? []);
      })
      .catch((cause) => {
        if (request === loadRequest && !sessionEnded(cause))
          setError(cause instanceof Error ? cause.message : "Could not load stack.");
      })
      .finally(() => {
        if (request === loadRequest) setLoading(false);
      });
  });

  // The environment to send: undefined keeps the stack's current one. Throws
  // with the offending line when the field doesn't parse.
  function environmentToSend(): Record<string, string> | undefined {
    if (!environmentEdited()) return undefined;
    return parseEnvironment(environmentText());
  }

  // Saves the form as a new stack, or as a new revision of the existing one.
  // Returns the stack's name and, when the save changed a running stack's
  // environment, the `up` the agent ran to apply it.
  async function persist(hostID: string): Promise<{ stackName: string; operation?: StackOperation }> {
    const existing = existingStack();
    const environment = environmentToSend();
    if (existing) {
      const result = await updateStack(hostID, existing, compose(), version(), environment);
      setVersion(result.version || version() + 1);
      setEnvironmentEdited(false);
      setSaved(true);
      setDirty(false);
      void loadStacks();
      return { stackName: existing, operation: result.operation };
    }
    const stackName = name().trim();
    await createStack(hostID, stackName, compose(), environment);
    setEnvironmentEdited(false);
    setCreated(stackName);
    setSaved(true);
    setDirty(false);
    void loadStacks();
    // The next save is an update, which needs the version just created.
    // Without it the save buttons stay disabled, but the stack exists.
    try {
      setVersion((await getStack(hostID, stackName)).version);
    } catch (cause) {
      if (!sessionEnded(cause)) setError("Saved, but its version could not be read; reopen the stack to edit it.");
    }
    return { stackName };
  }

  // Checks the form before a save; returns the problem, if any.
  function formProblem(): string {
    if (!stackNamePattern.test(name().trim()))
      return "Use 1–63 lowercase letters, digits, hyphens or underscores; start with a letter or digit.";
    if (!compose().trim()) return "Enter a Compose YAML document.";
    try {
      environmentToSend();
    } catch (cause) {
      return cause instanceof Error ? cause.message : "The environment is not valid.";
    }
    return "";
  }

  async function save(event: SubmitEvent) {
    event.preventDefault();
    if (busy()) return;
    const problem = formProblem();
    if (problem) {
      setError(problem);
      return;
    }
    setError("");
    setSaving(true);
    const hostID = currentHost();
    try {
      if (!saved()) {
        const { operation } = await persist(hostID);
        // A failed restart with the new environment stays on the page with its output.
        if (operation && !operation.success) {
          setDeployOutput(operation.output || "");
          setError("Saved, but restarting the stack with the new environment failed.");
          return;
        }
      }
      navigate(hostPath(hostID, "stacks"));
    } catch (cause) {
      if (!sessionEnded(cause)) setError(cause instanceof Error ? cause.message : "Could not save the stack.");
    } finally {
      setSaving(false);
    }
  }

  // A new revision may use different secrets; the panel's missing-value
  // warning follows the saved Compose file.
  async function refreshReferencedSecrets(hostID: string, stackName: string) {
    try {
      const stack = await getStack(hostID, stackName);
      setReferencedSecrets(hostID, stackName, stack.secrets ?? []);
    } catch (cause) {
      // Otherwise the warning stays as it was; the deploy result reports
      // missing values.
      sessionEnded(cause);
    }
  }

  async function saveAndStart(event: MouseEvent & { currentTarget: HTMLButtonElement }) {
    event.preventDefault();
    if (busy()) return;
    const form = event.currentTarget.form;
    if (!form?.reportValidity()) return;
    const problem = formProblem();
    if (problem) {
      setError(problem);
      return;
    }
    setError("");
    setDeploying(true);
    const hostID = currentHost();
    try {
      let target = existingStack() ?? name().trim();
      let applied: StackOperation | undefined;
      if (!saved()) {
        const updating = !!existingStack();
        const result = await persist(hostID);
        target = result.stackName;
        applied = result.operation;
        if (updating) await refreshReferencedSecrets(hostID, target);
      }
      // The save already brought the stack up with its new environment.
      if (applied && !applied.success)
        throw new StackOperationError("Start failed.", terminalText(applied.output ?? ""));
      const result = applied ?? (await deployStack(hostID, target));
      setDeployOutput(terminalText(result.output || "Deployment command completed."));
    } catch (cause) {
      if (sessionEnded(cause)) return;
      if (cause instanceof StackOperationError) setDeployOutput(cause.output);
      setError(
        `${saved() ? "Stack saved, but deployment failed" : "Could not save stack"}: ${cause instanceof Error ? cause.message : "Unknown error"}`,
      );
    } finally {
      setDeploying(false);
    }
  }

  return (
    <>
      <PageHeader
        crumbs={[
          ...hostCrumbs(currentHost()),
          { label: "Stacks", href: hostPath(currentHost(), "stacks") },
          ...(editedStack()
            ? [{ label: editedStack() ?? "", href: stackPath(currentHost(), editedStack() ?? "") }, { label: "Edit" }]
            : [{ label: "New" }]),
        ]}
        title={editedStack() ? `Edit ${editedStack()}` : "New stack"}
        actions={
          <Show when={editedStack() && version()}>
            <span class="header-chip">
              <span>Saved</span>
              <strong>v{version()}</strong>
            </span>
            <Show when={dirty()}>
              <span class="header-chip unsaved">Unsaved changes</span>
            </Show>
          </Show>
        }
      >
        <p class="subtitle">
          {editedStack()
            ? `Editing version ${version()}. Saving creates a new revision.`
            : `A Compose project that runs on ${currentHost()} with Podman Compose.`}
          {saved() ? " Changes saved. You can retry Start without saving another version." : ""}
        </p>
      </PageHeader>
      <StackStoreNotice />
      <section class="panel resource-page stack-create">
        <Show when={loading()}>
          <p>Loading stack…</p>
        </Show>
        <form onSubmit={save}>
          <label>
            Stack name
            <input
              required
              maxLength={63}
              pattern="[a-z0-9][a-z0-9_\-]{0,62}"
              value={name()}
              onInput={(event) => setName(event.currentTarget.value)}
              readOnly={!!existingStack()}
              disabled={busy()}
              placeholder="my-stack"
            />
          </label>
          <label>
            Compose YAML
            <textarea
              class="compose-editor"
              required
              rows={16}
              value={compose()}
              onInput={(event) => {
                setCompose(event.currentTarget.value);
                setSaved(false);
                setDirty(true);
              }}
              disabled={busy()}
              placeholder={"services:\n  web:\n    image: quay.io/libpod/busybox:latest"}
              spellcheck={false}
            />
          </label>
          <label>
            Environment
            <textarea
              rows={6}
              value={environmentText()}
              onInput={(event) => {
                setEnvironmentText(event.currentTarget.value);
                setEnvironmentEdited(true);
                setSaved(false);
                setDirty(true);
              }}
              disabled={busy()}
              placeholder={"# KEY=VALUE, one per line; use ${KEY} in the Compose file\nTZ=Europe/Amsterdam"}
              spellcheck={false}
            />
          </label>
          <p class="stack-note">
            Values are substituted for <code>${"{KEY}"}</code> in the Compose file and stored in plain text with each
            revision. Saving a changed environment restarts the stack if it is running. Passwords and keys belong in
            stack secrets.
          </p>
          <Show when={error()}>
            <p class="form-error" role="alert">
              {error()}
            </p>
          </Show>
          <Show when={deployOutput()}>
            <pre class="stack-output">{deployOutput()}</pre>
          </Show>
          <div class="stack-actions">
            <button
              class="primary"
              type="button"
              onClick={saveAndStart}
              disabled={busy() || (!!existingStack() && !version())}
            >
              {deploying() ? "Starting…" : saved() ? "Retry start" : "Save and start"}
            </button>
            <button class="add-button" type="submit" disabled={busy() || (!!existingStack() && !version())}>
              {saving() ? "Saving…" : "Save without starting"}
            </button>
          </div>
        </form>
        <Show
          when={editedStack()}
          fallback={
            <Show when={secrets.store !== "unavailable"} fallback={<NoSecretStore />}>
              <p class="stack-note">
                Stacks can use Compose <code>secrets:</code>. Save the stack, then set the secret values on its edit
                page before starting it.
              </p>
            </Show>
          }
        >
          {(stackName) => <StackSecretsPanel hostID={currentHost()} stackName={stackName()} />}
        </Show>
      </section>
    </>
  );
}
