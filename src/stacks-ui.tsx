import { A, useNavigate } from "@solidjs/router";
import { createEffect, createSignal, For, Show } from "solid-js";
import { editStackPath, hostPath, newStackPath } from "./host-route";
import { createStack, deployStack, getStack, stackNamePattern, updateStack, type Stack } from "./stack-api";

type StackListProps = {
  hostID: string;
  stacks: Stack[];
  loading: boolean;
  error: string;
  query: string;
  refresh: () => void;
};

export function StackList(props: StackListProps) {
  const [busy, setBusy] = createSignal("");
  const [operationError, setOperationError] = createSignal("");
  const [operationOutput, setOperationOutput] = createSignal("");
  const filtered = () =>
    props.stacks.filter((stack) => `${stack.name} ${stack.project}`.toLowerCase().includes(props.query));
  async function start(name: string) {
    setBusy(name);
    setOperationError("");
    setOperationOutput("");
    const hostID = props.hostID;
    try {
      const result = await deployStack(hostID, name);
      if (props.hostID !== hostID) return;
      setOperationOutput(result.output || `${name}: deployment command completed.`);
      props.refresh();
    } catch (cause) {
      if (props.hostID === hostID) setOperationError(cause instanceof Error ? cause.message : "Could not start stack.");
    } finally {
      setBusy("");
    }
  }
  return (
    <section class="panel resource-page">
      <div class="panel-heading">
        <div>
          <p class="eyebrow">{props.hostID} / STACKS</p>
          <h2>Compose stacks</h2>
        </div>
        <div class="stack-actions">
          <button class="add-button" type="button" onClick={props.refresh} disabled={props.loading}>
            Refresh
          </button>
          <A class="add-button" href={newStackPath(props.hostID)}>
            Create stack
          </A>
        </div>
      </div>
      <Show when={props.error || operationError()}>
        <p class="form-error" role="alert">
          {props.error || operationError()}
        </p>
      </Show>
      <Show when={operationOutput()}>
        <pre class="stack-output">{operationOutput()}</pre>
      </Show>
      <Show
        when={props.loading}
        fallback={
          <Show
            when={filtered().length}
            fallback={
              <div class="resource-empty">
                {props.query ? "No stacks match this search." : "No saved stacks on this host."}
              </div>
            }
          >
            <div class="resource-list">
              <For each={filtered()}>
                {(stack) => (
                  <div class="resource-row">
                    <span class="resource-icon">▤</span>
                    <span class="resource-copy">
                      <strong>{stack.name}</strong>
                      <small>{stack.project}</small>
                    </span>
                    <span class="resource-count">
                      v{stack.version || 1} · {stack.status}
                    </span>
                    <A class="add-button" href={editStackPath(props.hostID, stack.name)}>
                      Edit
                    </A>
                    <button class="start" type="button" disabled={!!busy()} onClick={() => start(stack.name)}>
                      {busy() === stack.name ? "Starting…" : "Start"}
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
        Saved status does not indicate whether containers are running. Start runs Podman Compose on this host.
      </p>
    </section>
  );
}

type StackEditorProps = {
  hostID: string;
  stackName?: string;
  onSaved: () => void;
};

export function StackEditor(props: StackEditorProps) {
  const navigate = useNavigate();
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
    const hostID = props.hostID;
    const stackName = props.stackName;
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
        if (props.hostID !== hostID || props.stackName !== stackName) return;
        setCompose(stack.compose_yaml ?? "");
        setVersion(stack.version);
      })
      .catch((cause) => {
        if (props.hostID === hostID && props.stackName === stackName)
          setError(cause instanceof Error ? cause.message : "Could not load stack.");
      })
      .finally(() => {
        if (props.hostID === hostID && props.stackName === stackName) setLoading(false);
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
    const hostID = props.hostID;
    try {
      if (!saved()) {
        if (props.stackName) {
          await updateStack(hostID, stackName, compose(), version());
          setVersion(version() + 1);
        } else await createStack(hostID, stackName, compose());
        setSaved(true);
        props.onSaved();
      }
      navigate(hostPath(hostID, "stacks"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save the stack.");
    } finally {
      setSaving(false);
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
    const hostID = props.hostID;
    try {
      if (!saved()) {
        if (props.stackName) {
          await updateStack(hostID, stackName, compose(), version());
          setVersion(version() + 1);
        } else await createStack(hostID, stackName, compose());
        setSaved(true);
        props.onSaved();
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
      <A href={hostPath(props.hostID, "stacks")}>← Back to stacks</A>
      <h2>{props.stackName ? `Edit ${props.stackName}` : `Create stack on ${props.hostID}`}</h2>
      <p class="subtitle">
        {props.stackName
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
            readOnly={!!props.stackName}
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
            disabled={busy() || (!!props.stackName && !version())}
          >
            {deploying() ? "Starting…" : saved() ? "Retry start" : "Save and start"}
          </button>
          <button class="add-button" type="submit" disabled={busy() || (!!props.stackName && !version())}>
            {saving() ? "Saving…" : "Save without starting"}
          </button>
        </div>
      </form>
    </section>
  );
}
