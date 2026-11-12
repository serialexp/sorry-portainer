import { test } from "node:test";
import assert from "node:assert/strict";
import { UnauthorizedError } from "../src/fleet-api.ts";
import { checkedText, imageStatusLine } from "../src/image-updates.ts";
import {
  LoginExistsError,
  LoginsLockedError,
  deleteLogin,
  listLogins,
  retryHost,
  revealLogin,
  saveLogin,
} from "../src/registries-api.ts";
import {
  checkLoginForm,
  fleetLabel,
  hostLook,
  keyParts,
  loginScope,
  registryOf,
  savedBanner,
  scopeLabel,
  testBadge,
  waitingLoginNote,
} from "../src/registry-logins.ts";
import { SecretsUnavailableError, TooManyAttemptsError, WrongPassphraseError } from "../src/secrets-api.ts";
import { wrongPassphraseMessage, unlockFailure } from "../src/secret-unlock.ts";

const ok = (body, status = 200) => ({ ok: true, status, json: async () => body });
const failed = (status, text = "", headers = {}) => ({
  ok: false,
  status,
  text: async () => text,
  headers: { get: (name) => headers[name] ?? null },
});
const now = 1_800_000_000;
const iso = (secs) => new Date(secs * 1000).toISOString();

test("requests name the login in the body, never the path", async () => {
  const calls = [];
  const request = async (url, init) => {
    calls.push([url, init?.method ?? "GET", init?.body ? JSON.parse(init.body) : undefined, init?.cache]);
    if (url === "/api/registries")
      return init
        ? ok({ login: { key: "ghcr.io/myorg" }, test: { state: "ok" } })
        : ok({ state: "locked", set_id: "", logins: null, hosts: null });
    if (url.endsWith("/reveal")) return ok({ password: "tok3n" });
    if (url.endsWith("/replace")) return ok({ login: { key: "ghcr.io/myorg" }, test: { state: "no_agent" } });
    return { ok: true, status: url.endsWith("/retry") ? 202 : 204 };
  };
  assert.deepEqual(await listLogins(request), { state: "locked", set_id: "", logins: [], hosts: [] });
  await saveLogin({ key: "ghcr.io/myorg", username: "bot", password: "tok3n" }, false, request);
  await saveLogin({ key: "ghcr.io/myorg", username: "bot2", password: "" }, true, request);
  await deleteLogin("ghcr.io/myorg", request);
  assert.equal(await revealLogin("ghcr.io/myorg", "correct horse battery", request), "tok3n");
  await retryHost("local-a", request);
  assert.deepEqual(calls, [
    ["/api/registries", "GET", undefined, undefined],
    ["/api/registries", "POST", { key: "ghcr.io/myorg", username: "bot", password: "tok3n" }, undefined],
    ["/api/registries/replace", "POST", { key: "ghcr.io/myorg", username: "bot2", password: "" }, undefined],
    ["/api/registries/delete", "POST", { key: "ghcr.io/myorg" }, undefined],
    ["/api/registries/reveal", "POST", { key: "ghcr.io/myorg", passphrase: "correct horse battery" }, "no-store"],
    ["/api/registries/retry", "POST", { host: "local-a" }, undefined],
  ]);
});

test("each failure status maps to its error", async () => {
  const answering = (response) => async () => response;
  await assert.rejects(listLogins(answering(failed(401))), UnauthorizedError);
  await assert.rejects(listLogins(answering(failed(503, "secret store not configured"))), SecretsUnavailableError);
  await assert.rejects(deleteLogin("quay.io", answering(failed(423))), LoginsLockedError);
  await assert.rejects(
    saveLogin({ key: "quay.io", username: "x", password: "y" }, false, answering(failed(409, "exists"))),
    LoginExistsError,
  );
  await assert.rejects(
    saveLogin(
      { key: "quay.io", username: "x", password: "y" },
      false,
      answering(failed(400, "a login is for a registry")),
    ),
    {
      message: "a login is for a registry",
    },
  );
  // A wrong passphrase and the attempt limit read exactly like an unlock's.
  const wrong = await revealLogin("quay.io", "nope", answering(failed(403))).catch((e) => e);
  assert.ok(wrong instanceof WrongPassphraseError);
  assert.equal(unlockFailure(wrong, 0).error, wrongPassphraseMessage);
  const limited = await revealLogin(
    "quay.io",
    "nope",
    answering(failed(429, "slow down", { "Retry-After": "30" })),
  ).catch((e) => e);
  assert.ok(limited instanceof TooManyAttemptsError);
  assert.equal(limited.retryAfterSeconds, 30);
  await assert.rejects(revealLogin("quay.io", "", answering(ok({}))), {
    message: "Enter the secret store passphrase.",
  });
});

test("the key field says what a key covers, as the design does", () => {
  assert.equal(loginScope("  "), null);
  assert.deepEqual(loginScope("http://registry.local"), {
    warn: true,
    text: "Only HTTPS registries are supported. Plain-HTTP registries can’t be used.",
  });
  assert.equal(loginScope("HTTPS://ghcr.io").warn, true);
  assert.match(loginScope("docker.io").text, /every image on Docker Hub, including its official images/);
  assert.equal(
    loginScope("ghcr.io").text,
    "Registry login: used for every image on ghcr.io unless a namespace login is more specific.",
  );
  assert.equal(loginScope("ghcr.io/myorg").text, "Namespace login: used for images under ghcr.io/myorg/… only.");
  // Trailing slashes are dropped before saving, so the hint leaves them out too.
  assert.equal(loginScope("ghcr.io/myorg//").text, "Namespace login: used for images under ghcr.io/myorg/… only.");
  assert.equal(
    loginScope("ghcr.io/").text,
    "Registry login: used for every image on ghcr.io unless a namespace login is more specific.",
  );
  assert.equal(
    loginScope("docker.io/myorg").text,
    "Namespace login: used for images under docker.io/myorg/… only. Docker Hub’s official images (docker.io/library/…) are not covered.",
  );
  assert.equal(
    loginScope("docker.io/library").text,
    "Namespace login: used for images under docker.io/library/… only.",
  );
  assert.deepEqual([scopeLabel("ghcr.io"), scopeLabel("ghcr.io/myorg")], ["Whole registry", "Namespace"]);
  assert.deepEqual([registryOf("ghcr.io/myorg/team"), registryOf("quay.io")], ["ghcr.io", "quay.io"]);
});

test("the form is checked before it is sent", () => {
  const add = (key, username = "bot", password = "tok3n") =>
    checkLoginForm({ mode: "add", key, username, password }, ["ghcr.io/myorg"]);
  assert.deepEqual(add(" registry.example.net/team// "), { key: "registry.example.net/team", error: "" });
  assert.equal(add("").error, "Enter a registry, like ghcr.io, or a namespace, like docker.io/myorg.");
  assert.equal(
    add("https://ghcr.io").error,
    "Leave out https:// and write the registry name only, like ghcr.io/myorg.",
  );
  assert.equal(
    add("ghcr.io/my org").error,
    "Use a registry host, optionally followed by a path, like registry.example.net/team.",
  );
  assert.equal(
    add("ghcr.io/myorg/").error,
    "A login for ghcr.io/myorg already exists. Use Replace on its row instead.",
  );
  assert.equal(add("quay.io", " ").error, "Enter the username.");
  assert.equal(add("quay.io", "bot", "").error, "Enter the password or access token.");
  assert.equal(add("quay.io", "bot", "x".repeat(4096)).error, "");
  assert.equal(add("quay.io", "bot", "é".repeat(2049)).error, "The password or token is longer than 4 KiB.");
  // Replacing keeps the stored password when the field is empty.
  assert.equal(
    checkLoginForm({ mode: "replace", key: "ghcr.io/myorg", username: "bot", password: "" }, ["ghcr.io/myorg"]).error,
    "",
  );
});

test("the Last test pill", () => {
  assert.deepEqual(testBadge(undefined), {
    label: "Not tested yet",
    tone: "muted",
    title: "No host was online to test it",
  });
  const at = iso(now);
  assert.deepEqual(testBadge({ state: "ok", host: "local-a", at }), {
    label: "Works",
    tone: "ok",
    title: "Tested on local-a",
  });
  assert.equal(testBadge({ state: "failed", failure: "denied", host: "local-c", at }).label, "Refused");
  assert.equal(testBadge({ state: "failed", failure: "unreachable", host: "local-c", at }).tone, "warn");
  assert.equal(testBadge({ state: "failed", failure: "tls", host: "local-c", at }).label, "Certificate problem");
});

test("after a save, the banner says what the test found", () => {
  assert.deepEqual(savedBanner("ghcr.io/myorg", { state: "ok", host: "local-a" }), {
    title: "Saved and tested.",
    text: "local-a logged in to ghcr.io with it. Sending it to every host now.",
    tone: "ok",
    icon: "check",
    alert: false,
  });
  assert.equal(
    savedBanner("ghcr.io/myorg", { state: "failed", failure: "denied", host: "local-a" }).text,
    "ghcr.io refused this login when local-a tried it. Check the username and token, then use Replace.",
  );
  assert.equal(
    savedBanner("quay.io", { state: "failed", failure: "unreachable", host: "local-a" }).title,
    "Saved, but not tested.",
  );
  assert.match(
    savedBanner("quay.io", { state: "failed", failure: "tls", host: "local-a" }).text,
    /couldn’t verify the TLS certificate of quay\.io/,
  );
  assert.deepEqual(savedBanner("quay.io", { state: "no_agent" }), {
    title: "Saved.",
    text: "No host is online to test it. The first host that connects tests it and gets it.",
    tone: "plain",
    icon: "info",
    alert: false,
  });
});

test("each host's row on the page", () => {
  const row = (state, extra = {}) => ({ host: "local-a", online: state !== "offline", set_id: "s1", state, ...extra });
  assert.equal(hostLook(row("current", { at: iso(now - 180) }), false, now).detail, "Received 3 min ago.");
  // Reported at connect, so no receipt time.
  assert.equal(hostLook(row("current"), false, now).detail, "Confirmed by the agent.");
  assert.deepEqual(hostLook(row("sending"), false, now).busy, true);
  // The push error is in the row's detail, not a tooltip.
  const failedRow = row("failed", { error: "write /run/auth.json: no space left on device.", retry_at: iso(now + 40) });
  assert.deepEqual(hostLook(failedRow, false, now), {
    label: "Delivery failed",
    detail: "The agent couldn’t save the logins: write /run/auth.json: no space left on device. Trying again in 40 s.",
    tone: "danger",
    busy: false,
    canRetry: true,
  });
  assert.equal(
    hostLook({ ...failedRow, retry_at: iso(now + 290) }, false, now).detail,
    "The agent couldn’t save the logins: write /run/auth.json: no space left on device. Trying again in 5 min.",
  );
  assert.equal(
    hostLook({ ...failedRow, error: "", retry_at: iso(now - 1) }, false, now).detail,
    "The agent didn’t confirm the new set.",
  );
  assert.equal(hostLook(failedRow, true, now).canRetry, false);
  assert.equal(
    hostLook(row("offline", { last_seen: iso(now - 7200) }), false, now).detail,
    "Last seen 2 hours ago. It gets the current logins when it reconnects.",
  );
  assert.equal(hostLook(row("offline"), false, now).detail, "It gets the current logins when it reconnects.");
  assert.deepEqual(hostLook(row("missing", { set_id: "" }), true, now), {
    label: "Missing the logins",
    detail: "Rebooted while the secret store was locked, so it has none. It gets them when you unlock.",
    tone: "warn",
    busy: false,
    canRetry: false,
  });
  // Locked, holding the set from before the last change.
  assert.deepEqual(hostLook(row("missing"), true, now), {
    label: "Has older logins",
    detail: "It still uses the logins from before your last change. It gets the current set when you unlock.",
    tone: "warn",
    busy: false,
    canRetry: false,
  });
  assert.equal(hostLook(row("missing"), false, now).label, "Sending…");
});

test("the fleet pill counts hosts with the current set", () => {
  const r = (state) => ({ host: state, online: true, set_id: "", state });
  assert.deepEqual(fleetLabel([r("current"), r("current"), r("current")]), {
    text: "All 3 hosts up to date",
    tone: "ok",
  });
  assert.deepEqual(fleetLabel([r("current"), r("failed"), r("offline")]), {
    text: "1 of 3 hosts up to date",
    tone: "warn",
  });
  assert.equal(fleetLabel([r("current")]).text, "1 host up to date");
  assert.deepEqual(fleetLabel([]), { text: "No hosts yet", tone: "ok" });
});

test("a refused image says why, once the master explained it", () => {
  const refused = {
    service: "api",
    image: "ghcr.io/myorg/api:2.4",
    status: "unknown",
    failure: "denied",
    registry: "ghcr.io",
    checked_at: now - 300,
  };
  assert.equal(imageStatusLine(refused, now, "local-a").text, "No access");
  const notOnHost = { ...refused, login_key: "ghcr.io/myorg", login_state: "not_on_host" };
  // Login states need action, so they use the problem tone.
  assert.equal(imageStatusLine(refused, now, "local-a").tone, "problem");
  assert.deepEqual(imageStatusLine(notOnHost, now, "local-a"), {
    text: "Login not on this host yet",
    tone: "problem",
    title:
      "A login for ghcr.io/myorg is saved, but local-a doesn’t have it yet (offline, delivery failed, or rebooted while the store was locked). Checked again once it arrives.",
  });
  assert.equal(
    imageStatusLine({ ...notOnHost, login_state: "refused" }, now, "local-a").title,
    "local-a has the login for ghcr.io/myorg, but ghcr.io refused it. The token may be expired or missing pull rights. Replace it in Registry logins.",
  );
  const hub = {
    ...refused,
    image: "example/worker:1.8",
    registry: "docker.io",
    login_state: "none",
    login_key: undefined,
  };
  assert.equal(imageStatusLine(hub, now, "local-a").text, "No login for this registry");
  assert.match(imageStatusLine(hub, now, "local-a").title, /Docker Hub answers the same way/);
  assert.doesNotMatch(imageStatusLine({ ...hub, registry: "ghcr.io" }, now, "local-a").title, /Docker Hub/);

  assert.equal(checkedText([hub], now), "No login for docker.io · checked 5 min ago");
  assert.equal(checkedText([notOnHost], now), "Waiting for the ghcr.io/myorg login · checked 5 min ago");
  assert.equal(
    checkedText([{ ...notOnHost, login_state: "refused" }], now),
    "ghcr.io refused the login · checked 5 min ago",
  );
  assert.equal(checkedText([{ ...hub, checked_at: undefined }], now), "No login for docker.io");
  assert.equal(checkedText([refused], now), "ghcr.io refused access · checked 5 min ago");
});

test("a stack waiting for a registry login says which, never the agent's reason", () => {
  const waiting = { waiting_login: "ghcr.io/myorg/api", waiting_login_key: "ghcr.io/myorg" };
  assert.equal(
    waitingLoginNote({ ...waiting, waiting_login_state: "not_on_host" }, "local-a"),
    "Waiting for a registry login: local-a doesn’t have the login for ghcr.io/myorg yet. The stack starts as soon as it arrives.",
  );
  assert.equal(
    waitingLoginNote({ ...waiting, waiting_login_state: "refused" }, "local-a"),
    "Waiting for a registry login: ghcr.io refused the saved login for ghcr.io/myorg. Replace it in Settings, and the stack starts once it works.",
  );
  const none =
    "Waiting for a registry login: there’s no saved login for docker.io. Add one in Settings, or check the image name if it’s meant to be public.";
  assert.equal(waitingLoginNote({ waiting_login: "docker.io/myorg/app", waiting_login_state: "none" }, "local-a"), none);
  // A master without a secret store gives no state: no login can be saved, so none.
  assert.equal(waitingLoginNote({ waiting_login: "docker.io/myorg/app" }, "local-a"), none);
  // Waiting for something other than a login: the agent's reason shows.
  assert.equal(waitingLoginNote({}, "local-a"), undefined);
});

test("a key wraps only after a slash", () => {
  assert.deepEqual(keyParts("registry.example.net/platform-team/ci"), ["registry.example.net/", "platform-team/", "ci"]);
  assert.deepEqual(keyParts("ghcr.io"), ["ghcr.io"]);
});
