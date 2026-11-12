import { A } from "@solidjs/router";
import { createEffect, createSignal, For, Match, on, onCleanup, Show, Switch } from "solid-js";
import { hostPath, registriesPath } from "./host-route";
import {
  CheckIcon,
  CloseIcon,
  EyeIcon,
  EyeSlashIcon,
  InfoIcon,
  LockIcon,
  NoLockIcon,
  PlusIcon,
  SpinnerIcon,
  WarningIcon,
} from "./icons";
import { timeAgo } from "./image-updates";
import { PageHeader } from "./page-header";
import type { RegistryLogin } from "./registries-api";
import {
  askRow,
  clearFormError,
  closeForm,
  closeRow,
  dismissBanner,
  editForm,
  loadLogins,
  openAddForm,
  openRegistriesPage,
  openReplaceForm,
  registries,
  removeLogin,
  retryNow,
  showPassword,
  submitForm,
} from "./registries-store";
import { fleetLabel, hostLook, loginScope, scopeLabel, testBadge } from "./registry-logins";
import { StoreNotice } from "./secrets-ui";
import { secrets } from "./secrets-store";

// The fleet-wide Settings page, per the design board "Settings · Registry
// logins". Registry logins is its only section so far.

const unlocked = () => secrets.store === "unlocked";

/** /settings/registries: the header, the section tabs and the Registry logins section. */
export function SettingsPage() {
  onCleanup(openRegistriesPage());
  // Locking or unlocking from the sidebar changes what hosts can be sent.
  createEffect(
    on(
      () => secrets.store,
      () => void loadLogins(),
      { defer: true },
    ),
  );
  return (
    <div class="settings-page">
      <PageHeader crumbs={[{ label: "Settings", href: registriesPath }, { label: "Registry logins" }]} title="Settings">
        <p class="subtitle">Settings for the whole fleet. They apply to every host.</p>
      </PageHeader>
      <nav class="settings-tabs" aria-label="Settings sections">
        <A class="settings-tab" href={registriesPath}>
          Registry logins
          <span class="resource-count">{registries.logins.length}</span>
        </A>
      </nav>
      <Show
        when={registries.phase !== "unavailable"}
        fallback={
          <div class="no-store">
            <NoLockIcon />
            <div>
              <strong>This server has no secret store configured</strong>
              <span>Registry logins are kept in the secret store, so none can be saved on this server.</span>
            </div>
          </div>
        }
      >
        <StoreNotice
          locked="Unlock it to add, change or show logins and to send them to hosts. Hosts keep the logins they already have until they reboot."
          setup="Create it on the Hosts page before adding registry logins."
        />
        <Show when={unlocked() && registries.form} keyed>
          <LoginForm />
        </Show>
        <Show when={registries.banner}>
          {(banner) => (
            <div class={`login-banner ${banner().tone}`} role={banner().alert ? "alert" : "status"}>
              <Switch>
                <Match when={banner().icon === "check"}>
                  <CheckIcon />
                </Match>
                <Match when={banner().icon === "warning"}>
                  <WarningIcon size={18} />
                </Match>
                <Match when={banner().icon === "info"}>
                  <InfoIcon size={18} />
                </Match>
              </Switch>
              <span>
                <strong>{banner().title}</strong> {banner().text}
              </span>
              <button class="login-banner-close" type="button" aria-label="Dismiss" onClick={dismissBanner}>
                <CloseIcon size={13} />
              </button>
            </div>
          )}
        </Show>
        <Show when={registries.error}>
          <p class="form-error" role="alert">
            {registries.error}
          </p>
        </Show>
        <LoginsPanel />
        <HostsPanel />
      </Show>
    </div>
  );
}

/** Adding a login, or replacing a saved one's username and password. */
function LoginForm() {
  // The password stays in this component; the store never holds it.
  const [password, setPassword] = createSignal("");
  const [reveal, setReveal] = createSignal(false);
  const form = () => registries.form ?? { mode: "add" as const, key: "", username: "", error: "" };
  const replacing = () => form().mode === "replace";
  const scope = () => loginScope(form().key);
  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (await submitForm(password())) setPassword("");
  }
  return (
    <form
      class="login-form-card"
      onSubmit={submit}
      autocomplete="off"
      aria-labelledby="login-form-heading"
      onKeyDown={(event) => {
        if (event.key === "Escape") closeForm();
      }}
    >
      <div class="login-form-head">
        <h2 id="login-form-heading">{replacing() ? `Replace the login for ${form().key}` : "Add a registry login"}</h2>
        <button class="icon-button" type="button" aria-label="Close" onClick={closeForm}>
          <CloseIcon />
        </button>
      </div>
      <div class="login-field">
        <label for="login-key">Registry or namespace</label>
        <input
          id="login-key"
          class="login-input mono"
          classList={{ warn: !!scope()?.warn }}
          type="text"
          spellcheck={false}
          autocomplete="off"
          placeholder="ghcr.io/myorg"
          value={form().key}
          readOnly={replacing()}
          aria-describedby="login-key-help login-key-scope"
          onInput={(event) => editForm("key", event.currentTarget.value)}
          ref={(input) => {
            if (!replacing()) queueMicrotask(() => input.focus());
          }}
        />
        <span id="login-key-help" class="login-help">
          A whole registry (<code>ghcr.io</code>) or one namespace in it (<code>docker.io/myorg</code>). The most
          specific login wins. HTTPS registries only.
        </span>
        <Show when={scope()}>
          {(s) => (
            <span id="login-key-scope" class="login-scope" classList={{ warn: s().warn }}>
              <InfoIcon />
              <span>{s().text}</span>
            </span>
          )}
        </Show>
      </div>
      <div class="login-field-pair">
        <div class="login-field">
          <label for="login-user">Username</label>
          <input
            id="login-user"
            class="login-input"
            type="text"
            spellcheck={false}
            autocomplete="off"
            placeholder="ci-bot"
            value={form().username}
            onInput={(event) => editForm("username", event.currentTarget.value)}
            ref={(input) => {
              if (replacing()) queueMicrotask(() => input.focus());
            }}
          />
          <span class="login-help">Shown in the list, even while the store is locked.</span>
        </div>
        <div class="login-field">
          <label for="login-pass">Password or access token</label>
          <span class="login-password">
            <input
              id="login-pass"
              class="mono"
              type={reveal() ? "text" : "password"}
              spellcheck={false}
              autocomplete="new-password"
              placeholder={replacing() ? "Leave empty to keep the current one" : "Token or password"}
              value={password()}
              aria-describedby="login-pass-help"
              onInput={(event) => {
                setPassword(event.currentTarget.value);
                clearFormError();
              }}
            />
            <button
              type="button"
              aria-label={reveal() ? "Hide password" : "Show password"}
              onClick={() => setReveal(!reveal())}
            >
              <Show when={reveal()} fallback={<EyeIcon />}>
                <EyeSlashIcon />
              </Show>
            </button>
          </span>
          <span id="login-pass-help" class="login-help">
            {replacing()
              ? "Only a new value replaces it. Up to 4 KiB."
              : "An access token is better than your account password. Up to 4 KiB."}
          </span>
        </div>
      </div>
      <Show when={form().error}>
        <p class="form-error" role="alert">
          {form().error}
        </p>
      </Show>
      <div class="login-form-foot">
        <button class="store-solid login-save" type="submit" disabled={registries.saving}>
          <Show when={registries.saving}>
            <SpinnerIcon />
          </Show>
          {registries.saving ? "Saving and testing…" : replacing() ? "Save and test" : "Add and test"}
        </button>
        <button class="add-button" type="button" disabled={registries.saving} onClick={closeForm}>
          Cancel
        </button>
        <span class="login-help">
          Saving tests the login through an online host. A failed test doesn’t undo the save.
        </span>
      </div>
    </form>
  );
}

/** The saved logins, with Show, Replace and Delete on each. */
function LoginsPanel() {
  // Showing a password: the passphrase typed for the open prompt, and the
  // one password fetched. Kept in this component only, never in the store.
  const [passphrase, setPassphrase] = createSignal("");
  const [showError, setShowError] = createSignal("");
  const [showBusy, setShowBusy] = createSignal(false);
  const [shown, setShown] = createSignal<{ key: string; value: string; hideAt: number } | null>(null);
  const [clock, setClock] = createSignal(Date.now());
  const [copied, setCopied] = createSignal<"" | "copied" | "failed">("");
  let ticker: ReturnType<typeof setInterval> | undefined;

  function hide() {
    clearInterval(ticker);
    ticker = undefined;
    setShown(null);
    setCopied("");
  }
  function closePrompts() {
    closeRow();
    setPassphrase("");
    setShowError("");
    hide();
  }
  onCleanup(hide);
  // Locking the store (or the session ending) hides a shown password.
  createEffect(() => {
    if (!unlocked()) closePrompts();
  });
  // A login deleted or replaced meanwhile takes its shown password along.
  createEffect(() => {
    const current = shown();
    if (current && !registries.logins.some((login) => login.key === current.key)) hide();
  });

  function ask(key: string, mode: "ask" | "delete") {
    closePrompts();
    askRow(key, mode);
  }
  function replace(login: RegistryLogin) {
    closePrompts();
    openReplaceForm(login);
  }

  async function reveal(event: SubmitEvent, key: string) {
    event.preventDefault();
    if (showBusy()) return;
    if (!passphrase()) {
      setShowError("Enter the secret store passphrase.");
      return;
    }
    setShowBusy(true);
    setShowError("");
    const result = await showPassword(key, passphrase());
    setShowBusy(false);
    // Drop an answer for a prompt that was closed or moved meanwhile.
    if (!result || registries.row?.key !== key || registries.row.mode !== "ask") return;
    setPassphrase("");
    if ("error" in result) {
      setShowError(result.error);
      return;
    }
    closeRow();
    const hideAt = Date.now() + 30_000;
    setShown({ key, value: result.value, hideAt });
    setClock(Date.now());
    clearInterval(ticker);
    ticker = setInterval(() => {
      const now = Date.now();
      if (now >= hideAt) hide();
      else setClock(now);
    }, 1000);
  }
  const hideIn = () => {
    const current = shown();
    return current ? Math.max(0, Math.ceil((current.hideAt - clock()) / 1000)) : 0;
  };
  function copy(value: string) {
    if (!navigator.clipboard) {
      setCopied("failed");
      return;
    }
    navigator.clipboard.writeText(value).then(
      () => setCopied("copied"),
      () => setCopied("failed"),
    );
  }

  return (
    <section class="panel" aria-labelledby="logins-heading">
      <div class="panel-heading">
        <div class="login-panel-copy">
          <h2 id="logins-heading">Registry logins</h2>
          <p class="subtitle">
            One set for every host. Agents use them to pull private images and to check stacks for image updates.
            Without a login, hosts can only pull public images.
          </p>
          <Show when={secrets.store === "locked"}>
            <span class="login-readonly">
              <LockIcon size={14} />
              Read-only while the store is locked
            </span>
          </Show>
        </div>
        <button class="add-button solid login-add" type="button" disabled={!unlocked()} onClick={openAddForm}>
          <PlusIcon />
          Add login
        </button>
      </div>
      <Switch>
        <Match when={registries.phase === "loading"}>
          <div class="resource-empty">Loading registry logins…</div>
        </Match>
        <Match when={registries.logins.length}>
          <div class="data-scroll">
            <div class="data-table cols-logins">
              <div class="data-head">
                <span>Registry or namespace</span>
                <span>Username</span>
                <span>Last set</span>
                <span>Last test</span>
                <span class="right">Actions</span>
              </div>
              <For each={registries.logins}>
                {(login) => {
                  const badge = () => testBadge(login.last_test);
                  const mode = () => (registries.row?.key === login.key ? registries.row.mode : "");
                  const isShown = () => shown()?.key === login.key;
                  return (
                    <div class="login-group">
                      <div class="data-row">
                        <div class="login-key">
                          <strong class="mono">{login.key}</strong>
                          <small>{scopeLabel(login.key)}</small>
                        </div>
                        <span class="login-user">{login.username}</span>
                        <span class="cell-value">{timeAgo(registries.now - Date.parse(login.updated_at) / 1000)}</span>
                        <span class={`login-pill ${badge().tone}`} title={badge().title}>
                          <i class="dot" aria-hidden="true"></i>
                          {badge().label}
                        </span>
                        <div class="cell-actions login-actions">
                          <button
                            class="add-button"
                            type="button"
                            disabled={!unlocked()}
                            aria-expanded={mode() === "ask" || isShown()}
                            onClick={() => ask(login.key, "ask")}
                          >
                            Show
                          </button>
                          <button
                            class="add-button"
                            type="button"
                            disabled={!unlocked()}
                            onClick={() => replace(login)}
                          >
                            Replace
                          </button>
                          <button
                            class="add-button danger"
                            type="button"
                            disabled={!unlocked()}
                            onClick={() => ask(login.key, "delete")}
                          >
                            Delete
                          </button>
                        </div>
                      </div>
                      <Show when={mode() === "ask"}>
                        <form class="login-prompt" onSubmit={(event) => reveal(event, login.key)} autocomplete="off">
                          <span>
                            Re-enter the secret store passphrase to show the password for{" "}
                            <strong class="mono">{login.key}</strong>.
                          </span>
                          <div class="login-prompt-fields">
                            <div>
                              <input
                                classList={{ error: !!showError() }}
                                type="password"
                                autocomplete="current-password"
                                aria-label="Secret store passphrase"
                                placeholder="Passphrase"
                                value={passphrase()}
                                disabled={showBusy()}
                                onInput={(event) => {
                                  setPassphrase(event.currentTarget.value);
                                  setShowError("");
                                }}
                                onKeyDown={(event) => {
                                  if (event.key === "Escape") closePrompts();
                                }}
                                ref={(input) => queueMicrotask(() => input.focus())}
                              />
                              <button class="store-solid" type="submit" disabled={showBusy()}>
                                Show
                              </button>
                              <button class="add-button" type="button" onClick={closePrompts}>
                                Cancel
                              </button>
                            </div>
                            <Show when={showError()}>
                              <p class="login-prompt-error" role="alert">
                                {showError()}
                              </p>
                            </Show>
                          </div>
                        </form>
                      </Show>
                      <Show when={isShown() ? shown() : undefined}>
                        {(current) => (
                          <div class="login-shown">
                            <code aria-label={`Password for ${login.key}`}>{current().value}</code>
                            <span class="login-shown-timer">Hides in {hideIn()} s</span>
                            <button class="login-shown-copy" type="button" onClick={() => copy(current().value)}>
                              {copied() === "copied" ? "Copied" : copied() === "failed" ? "Couldn’t copy" : "Copy"}
                            </button>
                            <button class="login-shown-hide" type="button" onClick={hide}>
                              Hide
                            </button>
                          </div>
                        )}
                      </Show>
                      <Show when={mode() === "delete"}>
                        <div class="login-delete" role="alert">
                          <span>
                            Delete the login for <strong class="mono">{login.key}</strong>? Hosts drop it at their next
                            update. Running containers keep running, but private images from {login.key} can no longer
                            be pulled or checked.
                          </span>
                          <button class="add-button" type="button" onClick={closePrompts}>
                            Keep
                          </button>
                          <button
                            class="login-delete-confirm"
                            type="button"
                            disabled={!!registries.busy}
                            onClick={() => void removeLogin(login.key)}
                          >
                            Delete login
                          </button>
                        </div>
                      </Show>
                    </div>
                  );
                }}
              </For>
            </div>
          </div>
        </Match>
        <Match when={registries.phase === "ready"}>
          <div class="login-empty">
            <span class="login-empty-icon" aria-hidden="true">
              <LockIcon size={22} />
            </span>
            <h3>No registry logins yet</h3>
            <p>
              Hosts can pull public images only. Add a login to use private images, or to avoid Docker Hub’s limits for
              anonymous pulls.
            </p>
          </div>
        </Match>
      </Switch>
    </section>
  );
}

/** Whether each host has the current set, with Retry now after a failed delivery. */
function HostsPanel() {
  const locked = () => secrets.store === "locked";
  const fleet = () => fleetLabel(registries.hosts);
  return (
    <section class="panel" aria-labelledby="login-hosts-heading">
      <div class="panel-heading">
        <div>
          <h2 id="login-hosts-heading">On each host</h2>
          <span class="subtitle">Hosts hold the logins in memory and in a file that a reboot clears.</span>
        </div>
        <Show when={registries.hosts.length}>
          <span class={`login-pill ${fleet().tone}`}>
            <i class="dot" aria-hidden="true"></i>
            {fleet().text}
          </span>
        </Show>
      </div>
      <div class="data-table cols-login-hosts">
        <For
          each={registries.hosts}
          fallback={
            <Show when={registries.phase === "ready"}>
              <div class="resource-empty">No hosts are enrolled yet.</div>
            </Show>
          }
        >
          {(row) => {
            const look = () => hostLook(row, locked(), registries.now);
            return (
              <div class="data-row">
                <A class="login-host-name" href={hostPath(row.host, "stacks")}>
                  {row.host}
                </A>
                <div class="login-host-state">
                  <span class={`login-host-label ${look().tone}`}>
                    <Show when={look().busy} fallback={<i class="dot" aria-hidden="true"></i>}>
                      <SpinnerIcon size={12} />
                    </Show>
                    {look().label}
                  </span>
                  <Show when={look().detail}>
                    <span class="login-help">{look().detail}</span>
                  </Show>
                </div>
                <Show when={look().canRetry}>
                  <button
                    class="add-button"
                    type="button"
                    title={row.error}
                    disabled={!!registries.busy}
                    onClick={() => void retryNow(row.host)}
                  >
                    Retry now
                  </button>
                </Show>
              </div>
            );
          }}
        </For>
      </div>
    </section>
  );
}
