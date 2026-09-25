import { createEffect, createSignal, For, Show } from "solid-js";
import { type Delivery, minPassphraseLength, secretNamePattern } from "./secrets-api";
import {
  initializeSecretStore,
  loadStackSecrets,
  lockSecretStore,
  refreshStoreState,
  removeStackSecret,
  saveStackSecret,
  secrets,
  stackSecrets,
  unlockSecretStore,
} from "./secrets-store";

/** Host fields about secret support, as reported by the agent. */
export type HostSecretInfo = { swap_active?: boolean; secrets_ready?: boolean; secrets_problem?: string };

/** Warnings about a host's secret support; renders nothing when all is well. */
export function HostSecretWarnings(props: { host: HostSecretInfo }) {
  return (
    <>
      <Show when={props.host.secrets_ready === false || props.host.secrets_problem}>
        <p class="secret-warning" role="status">
          Stack secrets: {props.host.secrets_problem || "this agent cannot deliver stack secrets."}
        </p>
      </Show>
      <Show when={props.host.swap_active}>
        <p class="secret-warning" role="status">
          Swap is active on this host. Stack secrets live only in memory, but the kernel may write memory to the swap
          device. Turn off swap, or use encrypted swap, to keep secrets off disk.
        </p>
      </Show>
    </>
  );
}

/** Locks, unlocks or creates the master's secret store. */
export function SecretStoreBanner() {
  const [passphrase, setPassphrase] = createSignal("");
  const [confirm, setConfirm] = createSignal("");
  const [localError, setLocalError] = createSignal("");
  void refreshStoreState();

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    setLocalError("");
    let ok: boolean;
    if (secrets.store === "uninitialized") {
      if (passphrase().length < minPassphraseLength) {
        setLocalError(`Use a passphrase of at least ${minPassphraseLength} characters.`);
        return;
      }
      if (passphrase() !== confirm()) {
        setLocalError("The passphrases do not match.");
        return;
      }
      ok = await initializeSecretStore(passphrase());
    } else ok = await unlockSecretStore(passphrase());
    if (ok) {
      setPassphrase("");
      setConfirm("");
    }
  }

  return (
    <Show when={secrets.store !== "unknown" && secrets.store !== "unavailable"}>
      <section class={`secret-store ${secrets.store}`} aria-label="Secret store">
        <Show
          when={secrets.store !== "unlocked"}
          fallback={
            <div class="secret-store-row">
              <span>
                <strong>Secret store unlocked.</strong> Stack secrets are delivered to agents as they connect.
              </span>
              <button class="add-button" type="button" disabled={secrets.storeBusy} onClick={() => lockSecretStore()}>
                Lock
              </button>
            </div>
          }
        >
          <form class="secret-store-form" onSubmit={submit}>
            <p>
              <strong>
                {secrets.store === "uninitialized" ? "Create the secret store." : "The secret store is locked."}
              </strong>{" "}
              {secrets.store === "uninitialized"
                ? "Choose a passphrase. It encrypts every stack secret on this server and is needed after each restart. It cannot be recovered: if you lose it, you must set every secret again."
                : "Enter the passphrase to deliver stack secrets. Agents keep the secrets they already have until they restart."}
            </p>
            <input
              type="password"
              autocomplete={secrets.store === "uninitialized" ? "new-password" : "current-password"}
              aria-label="Secret store passphrase"
              placeholder="Passphrase"
              value={passphrase()}
              onInput={(event) => setPassphrase(event.currentTarget.value)}
              disabled={secrets.storeBusy}
              required
            />
            <Show when={secrets.store === "uninitialized"}>
              <input
                type="password"
                autocomplete="new-password"
                aria-label="Confirm passphrase"
                placeholder="Confirm passphrase"
                value={confirm()}
                onInput={(event) => setConfirm(event.currentTarget.value)}
                disabled={secrets.storeBusy}
                required
              />
            </Show>
            <button class="add-button" type="submit" disabled={secrets.storeBusy}>
              {secrets.storeBusy ? "Working…" : secrets.store === "uninitialized" ? "Create" : "Unlock"}
            </button>
          </form>
        </Show>
        <Show when={localError() || secrets.storeError}>
          <p class="form-error" role="alert">
            {localError() || secrets.storeError}
          </p>
        </Show>
      </section>
    </Show>
  );
}

function describeDelivery(delivery: Delivery | null): string {
  if (!delivery) return "Not sent to the agent since the server started.";
  const at = new Date(delivery.at).toLocaleString();
  const restarted =
    (delivery.result?.restarted?.length
      ? ` Restarted ${delivery.result.restarted.length} container(s) to use new values.`
      : "") + (delivery.result?.started ? " Started the stack, which was waiting for these secrets." : "");
  switch (delivery.state) {
    case "delivered":
      return `Delivered to the agent at ${at}.${restarted}`;
    case "pending":
      return `Waiting: ${delivery.error ?? "not delivered yet."}`;
    default:
      return `Delivery failed at ${at}: ${delivery.error ?? "unknown error"}${restarted}`;
  }
}

function formatSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KiB`;
}

/** Names, delivery state and value editing for one stack's secrets. */
export function StackSecretsPanel(props: { hostID: string; stackName: string }) {
  const state = () => stackSecrets(props.hostID, props.stackName);
  const [name, setName] = createSignal("");
  const [value, setValue] = createSignal("");
  const [formError, setFormError] = createSignal("");
  const [reveal, setReveal] = createSignal(false);
  createEffect(() => {
    setName("");
    setValue("");
    setReveal(false);
    setFormError("");
    void loadStackSecrets(props.hostID, props.stackName);
  });
  const stored = () => new Set(state().entries.map((entry) => entry.name));
  const missing = () => state().referenced.filter((secret) => !stored().has(secret));
  const unused = () => state().entries.filter((entry) => !state().referenced.includes(entry.name));
  const unlocked = () => secrets.store === "unlocked";

  async function save(event: SubmitEvent) {
    event.preventDefault();
    const secretName = name().trim();
    if (!secretNamePattern.test(secretName)) {
      setFormError("Use 1–64 letters, digits, dots, hyphens or underscores; start with a letter or digit.");
      return;
    }
    if (!value()) {
      setFormError("Enter a secret value.");
      return;
    }
    setFormError("");
    if (await saveStackSecret(props.hostID, props.stackName, secretName, value())) {
      setName("");
      setValue("");
      setReveal(false);
    }
  }

  function remove(secretName: string) {
    if (!window.confirm(`Delete secret ${secretName}? Containers that use it will not start again until it is set.`))
      return;
    void removeStackSecret(props.hostID, props.stackName, secretName);
  }

  return (
    <section class="stack-secrets" aria-label="Stack secrets">
      <h3>Stack secrets</h3>
      <p class="subtitle">
        Values are encrypted on the server and sent to this host's agent, which keeps them in memory only. Containers
        see them as files in <code>/run/secrets</code>, a memory-only mount. Use Compose <code>secrets:</code> with{" "}
        <code>external: true</code> (or an empty entry) to use one. Changing a value restarts the containers that use
        it.
      </p>
      <p class="stack-note">
        Not for secrets: the Compose file, its <code>environment:</code> entries and the stack's <code>env.json</code>{" "}
        settings are stored in plain text on the host.
      </p>
      <Show when={secrets.store === "unavailable"}>
        <p class="form-error">This server has no secret store configured.</p>
      </Show>
      <Show when={state().error}>
        <p class="form-error" role="alert">
          {state().error}
        </p>
      </Show>
      <Show when={missing().length}>
        <p class="secret-warning" role="status">
          The Compose file uses secrets with no value yet: {missing().join(", ")}. The stack will not start until they
          are set.
        </p>
      </Show>
      <Show when={state().loading && !state().entries.length}>
        <div class="resource-empty">Loading secrets…</div>
      </Show>
      <Show when={state().entries.length}>
        <div class="resource-list">
          <For each={state().entries}>
            {(entry) => (
              <div class="resource-row">
                <span class="resource-icon">⚿</span>
                <span class="resource-copy">
                  <strong>{entry.name}</strong>
                  <small>
                    {formatSize(entry.size)} · updated {new Date(entry.updated_at).toLocaleString()}
                    {unused().some((item) => item.name === entry.name) ? " · not used by the Compose file" : ""}
                  </small>
                </span>
                <button
                  class="add-button"
                  type="button"
                  disabled={!unlocked() || !!state().busy}
                  onClick={() => {
                    setName(entry.name);
                    setValue("");
                  }}
                >
                  Replace
                </button>
                <button
                  class="add-button"
                  type="button"
                  disabled={!unlocked() || !!state().busy}
                  onClick={() => remove(entry.name)}
                >
                  {state().busy === entry.name ? "Deleting…" : "Delete"}
                </button>
              </div>
            )}
          </For>
        </div>
      </Show>
      <p class={`secret-delivery ${state().delivery?.state ?? "none"}`}>{describeDelivery(state().delivery)}</p>
      <Show
        when={unlocked()}
        fallback={
          <Show when={secrets.store === "locked" || secrets.store === "uninitialized"}>
            <p class="stack-note">Unlock the secret store to set, replace or delete values.</p>
          </Show>
        }
      >
        <form class="secret-form" onSubmit={save} autocomplete="off">
          <label>
            Secret name
            <input
              value={name()}
              onInput={(event) => setName(event.currentTarget.value)}
              maxLength={64}
              placeholder="db_password"
              disabled={!!state().busy}
              list={`secret-names-${props.hostID}-${props.stackName}`}
              spellcheck={false}
            />
            <datalist id={`secret-names-${props.hostID}-${props.stackName}`}>
              <For each={missing()}>{(secret) => <option value={secret} />}</For>
            </datalist>
          </label>
          <label>
            Value
            <textarea
              class="secret-value"
              classList={{ masked: !reveal() }}
              rows={3}
              value={value()}
              onInput={(event) => setValue(event.currentTarget.value)}
              disabled={!!state().busy}
              spellcheck={false}
              autocomplete="off"
              placeholder="Stored exactly as typed, including trailing newlines."
            />
          </label>
          <Show when={formError()}>
            <p class="form-error" role="alert">
              {formError()}
            </p>
          </Show>
          <div class="stack-actions">
            <label class="secret-reveal">
              <input type="checkbox" checked={reveal()} onChange={(event) => setReveal(event.currentTarget.checked)} />
              Show value
            </label>
            <button class="add-button" type="submit" disabled={!!state().busy}>
              {state().busy && state().busy === name().trim() ? "Saving…" : "Save secret"}
            </button>
          </div>
        </form>
      </Show>
    </section>
  );
}
