import { A, useLocation, useNavigate } from "@solidjs/router";
import { createEffect, createSignal, For, Show } from "solid-js";
import { filteredStacks, fleet, loadStacks, searchTerm, sessionEnded } from "./fleet-store";
import { editStackPath, hostPath, newStackPath, parseHostPath } from "./host-route";
import { StackSecretsPanel } from "./secrets-ui";
import { setReferencedSecrets } from "./secrets-store";
import {
  createStack,
  deployStack,
  formatEnvironment,
  getStack,
  parseEnvironment,
  rebootLabel,
  stackNamePattern,
  stopStack,
  updateStack,
  type Stack,
  type StackOperation,
} from "./stack-api";

/** The selected host's stacks. */
export function StackList() {
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
  return (
    <section class="panel resource-page">
      <div class="panel-heading">
        <div>
          <p class="eyebrow">{fleet.selectedHost} / STACKS</p>
          <h2>Compose stacks</h2>
        </div>
        <div class="stack-actions">
          <button class="add-button" type="button" onClick={() => loadStacks()} disabled={fleet.stacksLoading}>
            Refresh
          </button>
          <A class="add-button" href={newStackPath(fleet.selectedHost)}>
            Create stack
          </A>
        </div>
      </div>
      <Show when={fleet.stacksError || operationError()}>
        <p class="form-error" role="alert">
          {fleet.stacksError || operationError()}
        </p>
      </Show>
      <Show when={operationOutput()}>
        <pre class="stack-output">{operationOutput()}</pre>
      </Show>
      <Show
        when={fleet.stacksLoading}
        fallback={
          <Show
            when={filteredStacks().length}
            fallback={
              <div class="resource-empty">
                {searchTerm() ? "No stacks match this search." : "No saved stacks on this host."}
              </div>
            }
          >
            <div class="resource-list">
              <For each={filteredStacks()}>
                {(stack) => (
                  <div class="resource-row">
                    <span class="resource-icon">▤</span>
                    <span class="resource-copy">
                      <strong>{stack.name}</strong>
                      <small>{stack.project}</small>
                      <Show when={stack.waiting}>
                        <small class="stack-waiting">{stack.waiting}</small>
                      </Show>
                    </span>
                    <span class="resource-count">
                      v{stack.version || 1} · {rebootLabel(stack)}
                    </span>
                    <A class="add-button" href={editStackPath(fleet.selectedHost, stack.name)}>
                      Edit
                    </A>
                    <button class="start" type="button" disabled={!!busy()} onClick={() => run(stack.name, "up")}>
                      {busyLabel(stack.name, "up", "Start", "Starting…")}
                    </button>
                    <button class="stop" type="button" disabled={!!busy()} onClick={() => run(stack.name, "down")}>
                      {busyLabel(stack.name, "down", "Stop", "Stopping…")}
                    </button>
                  </div>
                )}
              </For>
            </div>
          </Show>
        }
      >
        <div class="resource-empty">Loading stacks…</div>
      </Show>
      <p class="stack-note">
        This list does not show whether containers are running. Start runs Podman Compose on this host, and the agent
        starts the stack again after the host reboots until you stop it.
      </p>
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
      <A href={hostPath(currentHost(), "stacks")}>← Back to stacks</A>
      <h2>{editedStack() ? `Edit ${editedStack()}` : `Create stack on ${currentHost()}`}</h2>
      <p class="subtitle">
        {editedStack()
          ? `Editing version ${version()}. Saving creates a new revision.`
          : "Save and start deploys to this host. You can also save a draft without starting it."}
        {saved() ? " Changes saved. You can retry Start without saving another version." : ""}
      </p>
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
          <p class="stack-note">
            Stacks can use Compose <code>secrets:</code>. Save the stack, then set the secret values on its edit page
            before starting it.
          </p>
        }
      >
        {(stackName) => <StackSecretsPanel hostID={currentHost()} stackName={stackName()} />}
      </Show>
    </section>
  );
}
