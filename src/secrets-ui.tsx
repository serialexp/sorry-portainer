import { A } from "@solidjs/router";
import { createEffect, createSignal, For, Match, Show, Switch } from "solid-js";
import { ClockIcon, KeyIcon, LockIcon, NoLockIcon, UnlockedIcon, WarningIcon } from "./icons";
import { retryMessage } from "./secret-unlock.ts";
import { type Delivery, minPassphraseLength, secretNamePattern } from "./secrets-api";
import {
  clearStoreError,
  initializeSecretStore,
  loadStackSecrets,
  lockSecretStore,
  refreshStoreState,
  removeStackSecret,
  requestStoreSetup,
  retryWait,
  saveStackSecret,
  secrets,
  showStackSecret,
  stackSecrets,
  takeSetupRequest,
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

/**
 * The passphrase form state shared by the sidebar panel and the stack page
 * banner. Both drive the same store, so the error line and the countdown
 * come from the store; only the typed passphrase is local.
 */
function createUnlockForm(onUnlocked?: () => void) {
  const [passphrase, setPassphrase] = createSignal("");
  let input: HTMLInputElement | undefined;
  const waiting = () => retryWait();
  const error = () => (waiting() > 0 ? "" : secrets.storeError);
  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (!passphrase() || waiting() > 0 || secrets.storeBusy) return;
    if (await unlockSecretStore(passphrase())) {
      setPassphrase("");
      onUnlocked?.();
      return;
    }
    // A wrong passphrase stays in the field, selected for retyping; the
    // countdown empties it.
    if (retryWait() > 0) setPassphrase("");
    else if (secrets.storeError) input?.select();
  }
  return {
    passphrase,
    waiting,
    error,
    submit,
    ref: (element: HTMLInputElement) => {
      input = element;
    },
    focus: () => input?.focus(),
    onInput: (event: InputEvent & { currentTarget: HTMLInputElement }) => {
      setPassphrase(event.currentTarget.value);
      clearStoreError();
    },
    reset: () => setPassphrase(""),
  };
}

/**
 * The store's status in the sidebar footer, on every page: the only Lock
 * control, and an Unlock that expands in place. Not shown when the master
 * has no secret store.
 */
export function SecretStoreSidebar() {
  const [expanded, setExpanded] = createSignal(false);
  const form = createUnlockForm(() => setExpanded(false));
  void refreshStoreState();
  function expand() {
    clearStoreError();
    setExpanded(true);
    form.focus();
  }
  function collapse() {
    form.reset();
    clearStoreError();
    setExpanded(false);
  }
  // Unlocking elsewhere, or the store going away, closes the form.
  createEffect(() => {
    if (secrets.store !== "locked") setExpanded(false);
  });
  return (
    <Show when={secrets.store !== "unknown" && secrets.store !== "unavailable"}>
      <section class="store-panel" classList={{ unlocked: secrets.store === "unlocked" }} aria-label="Secret store">
        <Switch>
          <Match when={secrets.store === "locked" && expanded()}>
            <form
              class="store-panel-form"
              onSubmit={form.submit}
              onKeyDown={(event) => {
                if (event.key === "Escape") collapse();
              }}
            >
              <span class="store-panel-state">
                <LockIcon />
                Secrets locked
              </span>
              <input
                ref={(element) => {
                  form.ref(element);
                  queueMicrotask(() => element.focus());
                }}
                class="store-panel-input"
                classList={{ error: !!form.error() }}
                type="password"
                autocomplete="current-password"
                aria-label="Secret store passphrase"
                aria-describedby="store-panel-note"
                placeholder="Passphrase"
                value={form.passphrase()}
                onInput={form.onInput}
                disabled={form.waiting() > 0}
              />
              <Show when={form.error()}>
                <p id="store-panel-note" class="store-panel-error" role="alert">
                  {form.error()}
                </p>
              </Show>
              <Show when={form.waiting() > 0}>
                <p id="store-panel-note" class="store-panel-wait" role="status">
                  <ClockIcon />
                  {retryMessage(form.waiting())}
                </p>
              </Show>
              <div class="store-panel-buttons">
                <button class="store-panel-cancel" type="button" onClick={collapse}>
                  Cancel
                </button>
                <button class="store-panel-action" type="submit" disabled={form.waiting() > 0 || secrets.storeBusy}>
                  Unlock
                </button>
              </div>
            </form>
          </Match>
          <Match when={secrets.store === "locked"}>
            <div class="store-panel-row">
              <span class="store-panel-state">
                <LockIcon />
                Secrets locked
              </span>
              <button class="store-panel-action" type="button" onClick={expand}>
                Unlock
              </button>
            </div>
          </Match>
          <Match when={secrets.store === "unlocked"}>
            <div class="store-panel-row">
              <span class="store-panel-state">
                <UnlockedIcon />
                Secrets unlocked
              </span>
              <button
                class="store-panel-cancel"
                type="button"
                disabled={secrets.storeBusy}
                onClick={() => void lockSecretStore()}
              >
                Lock
              </button>
            </div>
            <Show when={secrets.storeError}>
              <p class="store-panel-error" role="alert">
                {secrets.storeError}
              </p>
            </Show>
          </Match>
          <Match when={secrets.store === "uninitialized"}>
            <div class="store-panel-row">
              <span class="store-panel-state">
                <KeyIcon />
                Secrets not set up
              </span>
              <A class="store-panel-action" href="/" onClick={() => requestStoreSetup()}>
                Set up
              </A>
            </div>
          </Match>
        </Switch>
      </section>
    </Show>
  );
}

/**
 * The create form, on the Hosts page above the host grid, while the master's
 * store doesn't exist yet. Once created the store is unlocked and it goes.
 */
export function CreateStoreCard() {
  const [passphrase, setPassphrase] = createSignal("");
  const [confirm, setConfirm] = createSignal("");
  const [localError, setLocalError] = createSignal("");
  let first: HTMLInputElement | undefined;
  // The sidebar's "Set up" asks for focus, from this page or another.
  createEffect(() => {
    if (secrets.store === "uninitialized" && takeSetupRequest())
      queueMicrotask(() => {
        first?.scrollIntoView({ block: "center" });
        first?.focus();
      });
  });
  const error = () => localError() || secrets.storeError;

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    setLocalError("");
    if (passphrase().length < minPassphraseLength) {
      setLocalError(`Use a passphrase of at least ${minPassphraseLength} characters.`);
      return;
    }
    if (passphrase() !== confirm()) {
      setLocalError("The passphrases do not match.");
      return;
    }
    if (await initializeSecretStore(passphrase())) {
      setPassphrase("");
      setConfirm("");
    }
  }
  function edited() {
    setLocalError("");
    clearStoreError();
  }

  return (
    <Show when={secrets.store === "uninitialized"}>
      <form id="create-store" class="store-create" onSubmit={submit}>
        <div class="store-create-head">
          <span class="store-create-icon" aria-hidden="true">
            <KeyIcon size={19} />
          </span>
          <div>
            <h2>Create the secret store</h2>
            <span>
              Choose a passphrase. It encrypts every stack secret on this server and is needed after each restart.
            </span>
          </div>
        </div>
        <div class="store-create-warning">
          <WarningIcon />
          <span>
            <strong>It can’t be recovered.</strong> If you lose the passphrase, you’ll have to set every secret again.
          </span>
        </div>
        <div class="store-create-fields">
          <label for="new-pass">
            Passphrase
            <input
              ref={first}
              id="new-pass"
              type="password"
              autocomplete="new-password"
              value={passphrase()}
              onInput={(event) => {
                setPassphrase(event.currentTarget.value);
                edited();
              }}
              disabled={secrets.storeBusy}
            />
          </label>
          <label for="confirm-pass">
            Confirm passphrase
            <input
              id="confirm-pass"
              type="password"
              autocomplete="new-password"
              aria-describedby="create-help"
              value={confirm()}
              onInput={(event) => {
                setConfirm(event.currentTarget.value);
                edited();
              }}
              disabled={secrets.storeBusy}
            />
          </label>
          <button class="store-solid" type="submit" disabled={secrets.storeBusy}>
            Create store
          </button>
        </div>
        <p
          id="create-help"
          class="store-create-help"
          classList={{ error: !!error() }}
          role={error() ? "alert" : undefined}
        >
          {error() || `At least ${minPassphraseLength} characters.`}
        </p>
      </form>
    </Show>
  );
}

/**
 * Under a stack page's header: an unlock banner while the store is locked,
 * or a pointer to the Hosts page while it isn't set up. Nothing otherwise.
 */
export function StackStoreNotice() {
  const form = createUnlockForm();
  return (
    <Switch>
      <Match when={secrets.store === "locked"}>
        <form class="store-banner" onSubmit={form.submit}>
          <div class="store-banner-main">
            <span class="store-banner-icon" aria-hidden="true">
              <LockIcon size={18} />
            </span>
            <div class="store-banner-copy">
              <strong>The secret store is locked</strong>
              <Show when={!form.error() && !form.waiting()}>
                <span>
                  Unlock it to set values and deliver this stack’s secrets. Agents keep the secrets they already have
                  until they restart.
                </span>
              </Show>
            </div>
            <div class="store-banner-fields">
              <input
                ref={form.ref}
                classList={{ error: !!form.error() }}
                type="password"
                autocomplete="current-password"
                aria-label="Secret store passphrase"
                aria-describedby="store-banner-note"
                placeholder="Passphrase"
                value={form.passphrase()}
                onInput={form.onInput}
                disabled={form.waiting() > 0}
              />
              <button class="store-solid" type="submit" disabled={form.waiting() > 0 || secrets.storeBusy}>
                Unlock
              </button>
            </div>
          </div>
          <Show when={form.error()}>
            <p id="store-banner-note" class="store-banner-note error" role="alert">
              {form.error()}
            </p>
          </Show>
          <Show when={form.waiting() > 0}>
            <p id="store-banner-note" class="store-banner-note" role="status">
              <ClockIcon size={14} />
              {retryMessage(form.waiting())}
            </p>
          </Show>
        </form>
      </Match>
      <Match when={secrets.store === "uninitialized"}>
        <div class="store-setup-line">
          <KeyIcon size={18} />
          <span>
            <strong>The secret store isn’t set up yet.</strong> Create it on the Hosts page before setting secret
            values.
          </span>
          <A class="add-button" href="/" onClick={() => requestStoreSetup()}>
            Set up
          </A>
        </div>
      </Match>
    </Switch>
  );
}

/** Where secrets come up on a master with no secret store at all. */
export function NoSecretStore() {
  return (
    <div class="no-store">
      <NoLockIcon />
      <div>
        <strong>This server has no secret store configured</strong>
        <span>
          Stacks here can’t use Compose secrets. A stack whose Compose file uses <code>secrets:</code> won’t start.
        </span>
      </div>
    </div>
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
  // Showing a stored value: the secret whose passphrase prompt is open, and
  // the one value fetched. Kept in this component only, never in the store.
  const [asking, setAsking] = createSignal("");
  const [showPassphrase, setShowPassphrase] = createSignal("");
  const [showBusy, setShowBusy] = createSignal(false);
  const [showError, setShowError] = createSignal("");
  const [shown, setShown] = createSignal<{ name: string; value: string } | null>(null);
  function hideShown() {
    setAsking("");
    setShowPassphrase("");
    setShowError("");
    setShown(null);
  }
  createEffect(() => {
    setName("");
    setValue("");
    setReveal(false);
    setFormError("");
    hideShown();
    void loadStackSecrets(props.hostID, props.stackName);
  });
  // Locking the store (or the session ending) hides any shown value.
  createEffect(() => {
    if (secrets.store !== "unlocked") hideShown();
  });
  const stored = () => new Set(state().entries.map((entry) => entry.name));
  const missing = () => state().referenced.filter((secret) => !stored().has(secret));
  const unused = () => state().entries.filter((entry) => !state().referenced.includes(entry.name));
  const unlocked = () => secrets.store === "unlocked";

  function ask(secretName: string) {
    hideShown();
    setAsking(secretName);
  }

  async function show(event: SubmitEvent) {
    event.preventDefault();
    const secretName = asking();
    const passphrase = showPassphrase();
    if (!secretName || showBusy()) return;
    if (!passphrase) {
      setShowError("Enter the secret store passphrase.");
      return;
    }
    setShowBusy(true);
    setShowError("");
    const host = props.hostID;
    const stack = props.stackName;
    const result = await showStackSecret(host, stack, secretName, passphrase);
    setShowBusy(false);
    // Drop an answer for a prompt that was closed or moved meanwhile.
    if (!result || asking() !== secretName || host !== props.hostID || stack !== props.stackName) return;
    setShowPassphrase("");
    if ("error" in result) {
      setShowError(result.error);
      return;
    }
    setAsking("");
    setShown({ name: secretName, value: result.value });
  }

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
      if (shown()?.name === secretName) hideShown();
      setName("");
      setValue("");
      setReveal(false);
    }
  }

  function remove(secretName: string) {
    if (!window.confirm(`Delete secret ${secretName}? Containers that use it will not start again until it is set.`))
      return;
    if (shown()?.name === secretName || asking() === secretName) hideShown();
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
      <Show when={secrets.store !== "unavailable"} fallback={<NoSecretStore />}>
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
                <>
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
                      disabled={!unlocked() || showBusy()}
                      aria-expanded={asking() === entry.name || shown()?.name === entry.name}
                      onClick={() =>
                        asking() === entry.name || shown()?.name === entry.name ? hideShown() : ask(entry.name)
                      }
                    >
                      {shown()?.name === entry.name ? "Hide" : "Show"}
                    </button>
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
                  <Show when={asking() === entry.name}>
                    <form class="secret-shown" onSubmit={show} autocomplete="off">
                      <p class="stack-note">
                        Enter the secret store passphrase to show <strong>{entry.name}</strong>. The server logs that it
                        was shown.
                      </p>
                      <div class="stack-actions">
                        <input
                          type="password"
                          autocomplete="current-password"
                          aria-label={`Passphrase to show ${entry.name}`}
                          placeholder="Passphrase"
                          value={showPassphrase()}
                          onInput={(event) => setShowPassphrase(event.currentTarget.value)}
                          disabled={showBusy()}
                          ref={(input) => queueMicrotask(() => input.focus())}
                        />
                        <button class="add-button" type="submit" disabled={showBusy()}>
                          {showBusy() ? "Checking…" : "Show value"}
                        </button>
                        <button class="add-button" type="button" disabled={showBusy()} onClick={hideShown}>
                          Cancel
                        </button>
                      </div>
                      <Show when={showError()}>
                        <p class="form-error" role="alert">
                          {showError()}
                        </p>
                      </Show>
                    </form>
                  </Show>
                  <Show when={shown()?.name === entry.name ? shown() : undefined}>
                    {(item) => (
                      <div class="secret-shown">
                        <textarea
                          class="secret-value"
                          readOnly
                          rows={Math.min(8, Math.max(1, item().value.split("\n").length))}
                          value={item().value}
                          aria-label={`Value of ${entry.name}`}
                          spellcheck={false}
                        />
                      </div>
                    )}
                  </Show>
                </>
              )}
            </For>
          </div>
        </Show>
        <p class={`secret-delivery ${state().delivery?.state ?? "none"}`}>{describeDelivery(state().delivery)}</p>
        <Show when={secrets.store === "locked"}>
          <p class="secret-locked-note">
            <LockIcon size={14} />
            Unlock the secret store to set, replace or delete values.
          </p>
        </Show>
        <form class="secret-form" onSubmit={save} autocomplete="off">
          <fieldset disabled={!unlocked()}>
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
                <input
                  type="checkbox"
                  checked={reveal()}
                  onChange={(event) => setReveal(event.currentTarget.checked)}
                />
                Show value
              </label>
              <button class="add-button" type="submit" disabled={!!state().busy}>
                {state().busy && state().busy === name().trim() ? "Saving…" : "Save secret"}
              </button>
            </div>
          </fieldset>
        </form>
      </Show>
    </section>
  );
}
