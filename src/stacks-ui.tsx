import { A, useLocation, useNavigate } from "@solidjs/router";
import { createEffect, createSignal, For, Match, Show, Switch } from "solid-js";
import { filteredStacks, fleet, loadStacks, searchTerm, sessionEnded } from "./fleet-store";
import { editStackPath, hostPath, newStackPath, parseHostPath, stackPath } from "./host-route";
import { HealthNote } from "./health-note";
import { BackIcon, ChevronRightIcon, PlayIcon, StackIcon, StopIcon } from "./icons";
import { NoSecretStore, StackSecretsPanel, StackStoreNotice } from "./secrets-ui";
import {
  type StackRunState,
  type StackService,
  canStart,
  serviceStateLabel,
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
  stackNamePattern,
  stopStack,
  updateStack,
  type Stack,
  type StackOperation,
} from "./stack-api";

/** A stack's services, one row per container. */
function ServiceTable(props: { name: string; services: StackService[] }) {
  return (
    <div class="stack-services" role="table" aria-label={`Services of ${props.name}`}>
      <div class="stack-service-row stack-service-head" role="row">
        <span role="columnheader">Service</span>
        <span role="columnheader">Container</span>
        <span role="columnheader">Image</span>
        <span role="columnheader">State</span>
      </div>
      <For each={props.services}>{(svc) => <ServiceRow service={svc} />}</For>
    </div>
  );
}

/** The chip style for a stack's run state; partial, degraded and invalid need attention. */
function runChipClass(tone: StackRunState["tone"]): string {
  switch (tone) {
    case "running":
      return "state";
    case "partial":
    case "degraded":
    case "invalid":
      return "warn-chip";
    default:
      return "state stopped";
  }
}

/** One service row of a stack card: its container, image and state. */
function ServiceRow(props: { service: StackService }) {
  return (
    <div class="stack-service-row" role="row">
      <span class="stack-service-name" role="cell">
        {props.service.service}
      </span>
      <span class="cell-muted" role="cell">
        {props.service.container || "—"}
      </span>
      <span class="chip-code" role="cell">
        {props.service.image || "—"}
      </span>
      <span role="cell">
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
    </div>
  );
}

/** Start and stop for a host's stacks, with the busy state and output a page shows. */
export function createStackRunner() {
  const [busy, setBusy] = createSignal("");
  const [operationError, setOperationError] = createSignal("");
  const [operationOutput, setOperationOutput] = createSignal("");
  const [busyOperation, setBusyOperation] = createSignal<"up" | "down">("up");
  async function run(name: string, operation: "up" | "down") {
    const verb = operation === "up" ? "start" : "stop";
    const question = `Stop ${name}? Its containers are removed (volumes are kept) and it stays stopped after a reboot.`;
    if (operation === "down" && !window.confirm(question)) return;
    setBusy(name);
    setBusyOperation(operation);
    setOperationError("");
    setOperationOutput("");
    const hostID = fleet.selectedHost;
    try {
      const result = operation === "up" ? await deployStack(hostID, name) : await stopStack(hostID, name);
      if (fleet.selectedHost !== hostID) return;
      setOperationOutput(result.output || `${name}: ${verb} command completed.`);
    } catch (cause) {
      if (fleet.selectedHost === hostID && !sessionEnded(cause))
        setOperationError(cause instanceof Error ? cause.message : `Could not ${verb} stack.`);
    } finally {
      setBusy("");
      // A failed start still changes what the stack comes back to after a reboot.
      if (fleet.selectedHost === hostID) void loadStacks();
    }
  }
  const busyLabel = (name: string, operation: "up" | "down", idle: string, active: string) =>
    busy() === name && busyOperation() === operation ? active : idle;
  return { busy, operationError, operationOutput, run, busyLabel };
}

/** The selected host's stacks, one row each. */
export function StackList() {
  const { busy, operationError, operationOutput, run, busyLabel } = createStackRunner();
  return (
    <>
      <Show when={fleet.stacksError || operationError()}>
        <p class="form-error" role="alert">
          {fleet.stacksError || operationError()}
        </p>
      </Show>
      <Show when={operationOutput()}>
        <pre class="stack-output">{operationOutput()}</pre>
      </Show>
      <Show when={!fleet.stacksLoading} fallback={<div class="resource-empty">Loading stacks…</div>}>
        <Switch
          fallback={
            <div class="empty-card">
              <span class="empty-icon" aria-hidden="true">
                <StackIcon size={24} />
              </span>
              <h3>No stacks on {fleet.selectedHost}</h3>
              <p>Compose projects running on this host will show up here.</p>
            </div>
          }
        >
          <Match when={filteredStacks().length}>
            <section class="stack-rows" aria-label={`Stacks on ${fleet.selectedHost}`}>
              <For each={filteredStacks()}>
                {(stack) => {
                  const runState = () => stackRunState(stack);
                  const pill = () => stackPill(runState());
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
                      <span class={`stack-pill ${pill().tone}`} title={serviceSummary(stack.services ?? [])}>
                        <span class="pill-dot" aria-hidden="true"></span>
                        {pill().text}
                      </span>
                      <div class="stack-row-actions">
                        <Show when={runState().running === 0}>
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
                        <Show when={runState().running > 0}>
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
                        <p class="stack-row-note" role="status">
                          The Compose file does not parse. Open Edit to fix it; the stack cannot start until it does.
                        </p>
                      </Show>
                      <Show when={stack.waiting}>
                        <p class="stack-row-note" role="status">
                          {stack.waiting}
                        </p>
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
  });

  // Start stays available until the stack has loaded and while any service is not running.
  const startOffered = () => {
    const current = stack();
    return !current || canStart(stackRunState(current));
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
        setActionOutput(saved.operation.output || "");
      }
    } catch (cause) {
      if (!sessionEnded(cause)) setActionError(cause instanceof Error ? cause.message : "Could not restore revision.");
    } finally {
      setRevisionBusy(0);
      await load();
    }
  }

  return (
    <section class="panel resource-page stack-detail">
      <div class="stack-detail-head">
        <div class="stack-detail-title">
          <A class="stack-detail-back" href={hostPath(hostID(), "stacks")}>
            <BackIcon /> All stacks
          </A>
          <Show when={stack()} fallback={<h2>{name()}</h2>}>
            {(current) => {
              const runState = () => stackRunState(current());
              return (
                <>
                  <h2>{current().name}</h2>
                  <span class="stack-card-meta">
                    compose · {current().project} · v{current().version}
                  </span>
                  <div class="stack-detail-chips">
                    <span class={runChipClass(runState().tone)}>{runState().label}</span>
                    <span class="stack-card-reboot">{rebootLabel(current())}</span>
                  </div>
                </>
              );
            }}
          </Show>
        </div>
        <div class="stack-card-actions">
          <A class="add-button" href={editStackPath(hostID(), name())}>
            Edit
          </A>
          <Show when={startOffered()}>
            <button class="start" type="button" disabled={!!runner.busy() || !stack()} onClick={() => operate("up")}>
              {runner.busyLabel(name(), "up", "Start stack", "Starting…")}
            </button>
          </Show>
          <button class="stop" type="button" disabled={!!runner.busy() || !stack()} onClick={() => operate("down")}>
            {runner.busyLabel(name(), "down", "Stop stack", "Stopping…")}
          </button>
        </div>
      </div>
      <StackStoreNotice />
      <Show when={loadError()}>
        <p class="form-error" role="alert">
          {loadError()}
        </p>
      </Show>
      <Show when={runner.operationError() || actionError()}>
        <p class="form-error" role="alert">
          {runner.operationError() || actionError()}
        </p>
      </Show>
      <Show when={runner.operationOutput() || actionOutput()}>
        <pre class="stack-output">{runner.operationOutput() || actionOutput()}</pre>
      </Show>
      <Show when={loading() && !stack()}>
        <p class="resource-empty">Loading stack…</p>
      </Show>
      <Show when={stack()?.waiting}>
        <p class="stack-card-waiting" role="status">
          {stack()?.waiting}
        </p>
      </Show>
      <Show when={stack()?.services?.length}>
        <ServiceTable name={name()} services={stack()?.services ?? []} />
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
      void loadStacks();
      return { stackName: existing, operation: result.operation };
    }
    const stackName = name().trim();
    await createStack(hostID, stackName, compose(), environment);
    setEnvironmentEdited(false);
    setCreated(stackName);
    setSaved(true);
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
      if (applied && !applied.success) throw new Error(applied.output || "Deploy failed.");
      const result = applied ?? (await deployStack(hostID, target));
      setDeployOutput(result.output || "Deployment command completed.");
    } catch (cause) {
      if (sessionEnded(cause)) return;
      setError(
        `${saved() ? "Stack saved, but deployment failed" : "Could not save stack"}: ${cause instanceof Error ? cause.message : "Unknown error"}`,
      );
    } finally {
      setDeploying(false);
    }
  }

  return (
    <section class="panel resource-page stack-create">
      <A class="stack-detail-back" href={hostPath(currentHost(), "stacks")}>
        <BackIcon /> Back to stacks
      </A>
      <h2>{editedStack() ? `Edit ${editedStack()}` : `Create stack on ${currentHost()}`}</h2>
      <p class="subtitle">
        {editedStack()
          ? `Editing version ${version()}. Saving creates a new revision.`
          : "Save and start deploys to this host. You can also save a draft without starting it."}
        {saved() ? " Changes saved. You can retry Start without saving another version." : ""}
      </p>
      <StackStoreNotice />
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
            }}
            disabled={busy()}
            placeholder={"# KEY=VALUE, one per line; use ${KEY} in the Compose file\nTZ=Europe/Amsterdam"}
            spellcheck={false}
          />
        </label>
        <p class="stack-note">
          Values are substituted for <code>${"{KEY}"}</code> in the Compose file and stored in plain text with each
          revision. Saving a changed environment restarts the stack if it is running. Passwords and keys belong in stack
          secrets.
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
              Stacks can use Compose <code>secrets:</code>. Save the stack, then set the secret values on its edit page
              before starting it.
            </p>
          </Show>
        }
      >
        {(stackName) => <StackSecretsPanel hostID={currentHost()} stackName={stackName()} />}
      </Show>
    </section>
  );
}
