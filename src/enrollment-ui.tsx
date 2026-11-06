import { createSignal, For, onCleanup, onMount, Show } from "solid-js";
import {
  certificateState,
  type EnrolledHost,
  hostIDPattern,
  type TokenLifetime,
  tokenLifetimes,
} from "./enrollment-api";
import {
  dismissIssued,
  enrollment,
  issueToken,
  loadEnrollment,
  remove,
  resetEnrollment,
  revoke,
} from "./enrollment-store";
import { fleet, searchTerm } from "./fleet-store";

const formatTime = (iso: string) => new Date(iso).toLocaleString();

const certificateNotes = {
  valid: "renews automatically",
  renewing: "renewed; waiting for the agent to reconnect",
  overdue: "renewal overdue: check the agent's log",
  expired: "expired: enroll this host again",
} as const;

/** Copies text when the browser allows it (secure contexts only). */
function CopyButton(props: { text: string; label: string }) {
  const [copied, setCopied] = createSignal(false);
  let reset: ReturnType<typeof setTimeout> | undefined;
  onCleanup(() => clearTimeout(reset));
  return (
    <Show when={navigator.clipboard}>
      <button
        class="add-button"
        type="button"
        aria-label={props.label}
        onClick={async () => {
          await navigator.clipboard.writeText(props.text);
          setCopied(true);
          clearTimeout(reset);
          reset = setTimeout(() => setCopied(false), 2000);
        }}
      >
        {copied() ? "Copied" : "Copy"}
      </button>
    </Show>
  );
}

function IssuedToken() {
  return (
    <Show when={enrollment.issued}>
      {(issued) => (
        <section class="panel enroll-issued" aria-label="New enrollment token">
          <div class="panel-heading">
            <div>
              <p class="eyebrow">{issued().host_id} / NEW TOKEN</p>
              <h2>Enroll {issued().host_id}</h2>
            </div>
            <button class="add-button" type="button" onClick={dismissIssued}>
              Done
            </button>
          </div>
          <p class="secret-warning">
            This token is shown only once and works once, until {formatTime(issued().expires_at)}. Anyone who has it can
            enroll a host as {issued().host_id}.
          </p>
          <div class="enroll-copy">
            <label for="enrollment-token">Token</label>
            <span>
              <input
                id="enrollment-token"
                readOnly
                value={issued().token}
                onFocus={(event) => event.currentTarget.select()}
              />
              <CopyButton text={issued().token} label="Copy token" />
            </span>
          </div>
          <ol class="enroll-steps">
            <li>
              On the host, download <code>sorry-portainer-agent</code> from the release and run{" "}
              <code>sudo ./sorry-portainer-agent setup</code>. It installs the agent and creates its user.
            </li>
            <li>
              Run this command there and paste the token when it asks:
              <span class="enroll-command">
                <pre>{issued().command}</pre>
                <CopyButton text={issued().command} label="Copy command" />
              </span>
              <small>
                {issued().reenroll
                  ? `${issued().host_id} is already enrolled, so the command replaces its agent's certificate and keeps its settings and stacks. The agent connected now is disconnected once the new certificate is issued.`
                  : "The command refuses to overwrite an existing agent configuration; add --replace if this host was enrolled before."}
              </small>
            </li>
            <li>
              <Show
                when={issued().reenroll}
                fallback={
                  <>
                    Start the agent: <code>sudo systemctl enable --now sorry-portainer-agent</code>
                  </>
                }
              >
                Restart the agent so it uses the new certificate:{" "}
                <code>sudo systemctl restart sorry-portainer-agent</code>
              </Show>
            </li>
          </ol>
        </section>
      )}
    </Show>
  );
}

function EnrollForm() {
  const [hostID, setHostID] = createSignal("");
  const [ttl, setTTL] = createSignal<TokenLifetime>("1h");
  const [error, setError] = createSignal("");
  async function submit(event: SubmitEvent) {
    event.preventDefault();
    const id = hostID().trim();
    if (!hostIDPattern.test(id)) {
      setError("Use 1–128 letters, digits, '.', '_' or '-', starting with a letter or digit.");
      return;
    }
    setError("");
    if (await issueToken(id, ttl())) setHostID("");
  }
  return (
    <section class="panel dark-panel">
      <div class="dark-intro">
        <div class="panel-heading">
          <div>
            <p class="eyebrow">NEW HOST</p>
            <h2>Enroll a host</h2>
          </div>
        </div>
        <p class="subtitle">
          A token lets one agent enroll once as the host ID you choose. The host ID is how the dashboard names the host.
        </p>
      </div>
      <form class="enroll-form" onSubmit={submit}>
        <label>
          Host ID
          <input
            required
            maxLength={128}
            pattern="[A-Za-z0-9][A-Za-z0-9._\-]{0,127}"
            value={hostID()}
            onInput={(event) => setHostID(event.currentTarget.value)}
            disabled={!!enrollment.busy}
            placeholder="hel1"
            spellcheck={false}
          />
        </label>
        <label>
          Token lifetime
          <select
            value={ttl()}
            onChange={(event) => setTTL(event.currentTarget.value as TokenLifetime)}
            disabled={!!enrollment.busy}
          >
            <For each={tokenLifetimes}>{(lifetime) => <option value={lifetime.value}>{lifetime.label}</option>}</For>
          </select>
        </label>
        <button class="primary" type="submit" disabled={!!enrollment.busy}>
          {enrollment.busy === "issue" ? "Creating…" : "Create token"}
        </button>
      </form>
      <Show when={error()}>
        <p class="form-error" role="alert">
          {error()}
        </p>
      </Show>
    </section>
  );
}

function OutstandingTokens() {
  return (
    <section class="panel">
      <div class="panel-heading">
        <div>
          <p class="eyebrow">NOT USED YET</p>
          <h2>Outstanding tokens</h2>
        </div>
        <span class="resource-count">{enrollment.tokens.length}</span>
      </div>
      <Show
        when={enrollment.tokens.length}
        fallback={<div class="resource-empty">No tokens are waiting to be used.</div>}
      >
        <div class="resource-list enroll-list">
          <For each={enrollment.tokens}>
            {(token) => (
              <div class="resource-row">
                <span class="resource-icon">⚿</span>
                <span class="resource-copy">
                  <strong>{token.host_id}</strong>
                  <small>Expires {formatTime(token.expires_at)}</small>
                </span>
                <button class="stop" type="button" disabled={!!enrollment.busy} onClick={() => revoke(token.host_id)}>
                  {enrollment.busy === `revoke:${token.host_id}` ? "Revoking…" : "Revoke"}
                </button>
              </div>
            )}
          </For>
        </div>
      </Show>
    </section>
  );
}

function EnrolledHosts() {
  const hosts = () =>
    enrollment.hosts.filter(
      (host) => !searchTerm() || `${host.host_id} ${host.prefix}`.toLowerCase().includes(searchTerm()),
    );
  function confirmRemove(host: EnrolledHost) {
    const question = `Remove ${host.host_id}? Its agent is disconnected and cannot connect again until the host is enrolled again. Containers and stacks on the host are not touched.`;
    if (window.confirm(question)) void remove(host.host_id);
  }
  return (
    <section class="panel">
      <div class="panel-heading">
        <div>
          <p class="eyebrow">ALLOWED TO CONNECT</p>
          <h2>Enrolled hosts</h2>
        </div>
        <span class="resource-count">{hosts().length}</span>
      </div>
      <Show
        when={hosts().length}
        fallback={
          <div class="resource-empty">
            {searchTerm() ? "No enrolled hosts match this search." : "No hosts are enrolled yet."}
          </div>
        }
      >
        <div class="resource-list enroll-list">
          <For each={hosts()}>
            {(host) => {
              const state = () => certificateState(host, new Date());
              return (
                <div class="resource-row">
                  <span class="resource-icon">⌂</span>
                  <span class="resource-copy">
                    <strong>{host.host_id}</strong>
                    <small>
                      Prefix {host.prefix} · enrolled {formatTime(host.enrolled_at)}
                    </small>
                    <small class={`certificate ${state()}`}>
                      Certificate until {formatTime(host.current_not_after)} · {certificateNotes[state()]}
                    </small>
                  </span>
                  <span class={host.online ? "online" : "offline"}>
                    <i class={`dot ${host.online ? "green" : "grey"}`}></i>
                    {host.online ? "Online" : "Offline"}
                  </span>
                  <button class="stop" type="button" disabled={!!enrollment.busy} onClick={() => confirmRemove(host)}>
                    {enrollment.busy === `remove:${host.host_id}` ? "Removing…" : "Remove"}
                  </button>
                </div>
              );
            }}
          </For>
        </div>
      </Show>
    </section>
  );
}

/** Enroll hosts, revoke tokens and remove hosts from the master's registry. */
export function EnrollmentPage() {
  onMount(() => {
    if (!fleet.demo) void loadEnrollment();
  });
  // The token's secret is not kept once the page closes.
  // Leaving the page drops the issued token, also one still being issued.
  onCleanup(resetEnrollment);
  return (
    <Show
      when={!fleet.demo}
      fallback={<div class="resource-empty">Enrollment needs the master; demo mode cannot enroll hosts.</div>}
    >
      <Show
        when={enrollment.availability !== "unavailable"}
        fallback={<div class="resource-empty">{enrollment.error}</div>}
      >
        <div class="enrollment-page">
          <div class="stack-actions enroll-toolbar">
            <button class="add-button" type="button" onClick={() => loadEnrollment()} disabled={enrollment.loading}>
              {enrollment.loading ? "Refreshing…" : "Refresh"}
            </button>
          </div>
          <Show when={enrollment.error}>
            <p class="form-error" role="alert">
              {enrollment.error}
            </p>
          </Show>
          <IssuedToken />
          <EnrollForm />
          <OutstandingTokens />
          <EnrolledHosts />
        </div>
      </Show>
    </Show>
  );
}
