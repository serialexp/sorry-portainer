import { A, useLocation, useNavigate } from "@solidjs/router";
import { createEffect, createSignal, For, Show } from "solid-js";
import { filteredStacks, fleet, loadStacks, searchTerm } from "./fleet-store";
import { editStackPath, hostPath, newStackPath, parseHostPath } from "./host-route";
import { StackSecretsPanel } from "./secrets-ui";
import { setReferencedSecrets } from "./secrets-store";
import {
  createStack,
  deployStack,
  getStack,
  rebootLabel,
  stackNamePattern,
  stopStack,
  updateStack,
  type Stack,
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
      if (fleet.selectedHost === hostID)
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
  const [version, setVersion] = createSignal(0);
  const [error, setError] = createSignal("");
  const [loading, setLoading] = createSignal(false);
  const [saving, setSaving] = createSignal(false);
  const [deploying, setDeploying] = createSignal(false);
  const [saved, setSaved] = createSignal(false);
  const [deployOutput, setDeployOutput] = createSignal("");
  const busy = () => saving() || deploying() || loading();
  createEffect(() => {
    const hostID = currentHost();
    const stackName = editedStack();
    setName(stackName ?? "");
    setCompose("");
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
        if (currentHost() !== hostID || editedStack() !== stackName) return;
        setCompose(stack.compose_yaml ?? "");
        setVersion(stack.version);
        setReferencedSecrets(hostID, stackName, stack.secrets ?? []);
      })
      .catch((cause) => {
        if (currentHost() === hostID && editedStack() === stackName)
          setError(cause instanceof Error ? cause.message : "Could not load stack.");
      })
      .finally(() => {
        if (currentHost() === hostID && editedStack() === stackName) setLoading(false);
      });
  });

  async function save(event: SubmitEvent) {
    event.preventDefault();
    if (busy()) return;
    const stackName = name().trim();
    if (!stackNamePattern.test(stackName)) {
      setError("Use 1–63 lowercase letters, digits, hyphens or underscores; start with a letter or digit.");
      return;
    }
    if (!compose().trim()) {
      setError("Enter a Compose YAML document.");
      return;
    }
    setError("");
    setSaving(true);
    const hostID = currentHost();
    try {
      if (!saved()) {
        if (editedStack()) {
          await updateStack(hostID, stackName, compose(), version());
          setVersion(version() + 1);
        } else await createStack(hostID, stackName, compose());
        setSaved(true);
        void loadStacks();
      }
      navigate(hostPath(hostID, "stacks"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save the stack.");
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
    } catch {
      // The warning stays as it was; the deploy result reports missing values.
    }
  }

  async function saveAndStart(event: MouseEvent & { currentTarget: HTMLButtonElement }) {
    event.preventDefault();
    if (busy()) return;
    const form = event.currentTarget.form;
    if (!form?.reportValidity()) return;
    const stackName = name().trim();
    if (!stackNamePattern.test(stackName) || !compose().trim()) {
      setError("Enter a valid stack name and Compose YAML document.");
      return;
    }
    setError("");
    setDeploying(true);
    const hostID = currentHost();
    try {
      if (!saved()) {
        if (editedStack()) {
          await updateStack(hostID, stackName, compose(), version());
          setVersion(version() + 1);
        } else await createStack(hostID, stackName, compose());
        setSaved(true);
        void loadStacks();
        if (editedStack()) await refreshReferencedSecrets(hostID, stackName);
      }
      const result = await deployStack(hostID, stackName);
      setDeployOutput(result.output || "Deployment command completed.");
    } catch (cause) {
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
            pattern="[a-z0-9][a-z0-9_-]{0,62}"
            value={name()}
            onInput={(event) => setName(event.currentTarget.value)}
            readOnly={!!editedStack()}
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
            disabled={busy() || (!!editedStack() && !version())}
          >
            {deploying() ? "Starting…" : saved() ? "Retry start" : "Save and start"}
          </button>
          <button class="add-button" type="submit" disabled={busy() || (!!editedStack() && !version())}>
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
