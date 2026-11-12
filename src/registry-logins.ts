// What the Registry logins page and the stack pages say about logins. The
// copy is the design board's ("Settings · Registry logins" and "Warnings and
// empty states"); everything here is pure so it can be tested.

import { timeAgo } from "./image-updates.ts";
import { type LoginHostStatus, type LoginTest, type LoginTestResult, maxLoginPassword } from "./registries-api.ts";

/** The registry of a key: everything before the first "/". */
export function registryOf(key: string): string {
  const slash = key.indexOf("/");
  return slash < 0 ? key : key.slice(0, slash);
}

/** "Whole registry" or "Namespace", under a key in the list. */
export function scopeLabel(key: string): string {
  return key.includes("/") ? "Namespace" : "Whole registry";
}

export type Scope = { warn: boolean; text: string };

/** What a typed key will be used for, or why it can't be used; null for an empty field. */
export function loginScope(raw: string): Scope | null {
  // Trailing slashes are dropped before saving (checkLoginForm), so the hint
  // describes the key as it will be saved.
  const key = raw.trim().replace(/\/+$/, "");
  if (!key) return null;
  if (/^http:\/\//i.test(key))
    return { warn: true, text: "Only HTTPS registries are supported. Plain-HTTP registries can’t be used." };
  if (/^https:\/\//i.test(key))
    return { warn: true, text: "Leave out https:// and write the registry name only, like ghcr.io/myorg." };
  const slash = key.indexOf("/");
  if (slash < 0) {
    if (key === "docker.io")
      return {
        warn: false,
        text: "Registry login: used for every image on Docker Hub, including its official images (docker.io/library/…).",
      };
    return {
      warn: false,
      text: `Registry login: used for every image on ${key} unless a namespace login is more specific.`,
    };
  }
  let text = `Namespace login: used for images under ${key}/… only.`;
  if (key.slice(0, slash) === "docker.io" && !key.startsWith("docker.io/library"))
    text += " Docker Hub’s official images (docker.io/library/…) are not covered.";
  return { warn: false, text };
}

export type LoginForm = { mode: "add" | "replace"; key: string; username: string; password: string };

/** The message for a login already saved, as the form and a 409 both say it. */
export function loginExistsMessage(key: string): string {
  return `A login for ${key} already exists. Use Replace on its row instead.`;
}

/**
 * Checks the form before it is sent. Returns the key as it is sent (trimmed,
 * without trailing slashes) and the first problem, or "" when there is none.
 * The master checks again and has the last word.
 */
export function checkLoginForm(form: LoginForm, savedKeys: readonly string[]): { key: string; error: string } {
  const key = form.key.trim().replace(/\/+$/, "");
  const scope = loginScope(key);
  let error = "";
  if (!key) error = "Enter a registry, like ghcr.io, or a namespace, like docker.io/myorg.";
  else if (scope?.warn) error = scope.text;
  else if (!/^[a-z0-9.-]+(:[0-9]+)?(\/[a-z0-9._-]+)*$/i.test(key))
    error = "Use a registry host, optionally followed by a path, like registry.example.net/team.";
  else if (form.mode === "add" && savedKeys.includes(key)) error = loginExistsMessage(key);
  else if (!form.username.trim()) error = "Enter the username.";
  else if (form.mode === "add" && !form.password) error = "Enter the password or access token.";
  else if (new TextEncoder().encode(form.password).length > maxLoginPassword)
    error = "The password or token is longer than 4 KiB.";
  return { key, error };
}

export type Tone = "ok" | "danger" | "warn" | "muted";

/** The Last test pill of a login. */
export function testBadge(test: LoginTest | undefined): { label: string; tone: Tone; title: string } {
  if (!test) return { label: "Not tested yet", tone: "muted", title: "No host was online to test it" };
  const title = `Tested on ${test.host}`;
  if (test.state === "ok") return { label: "Works", tone: "ok", title };
  switch (test.failure) {
    case "unreachable":
      return { label: "Unreachable", tone: "warn", title };
    case "tls":
      return { label: "Certificate problem", tone: "danger", title };
    default:
      return { label: "Refused", tone: "danger", title };
  }
}

export type Banner = {
  title: string;
  text: string;
  tone: Tone | "plain";
  icon: "check" | "warning" | "info";
  alert: boolean;
};

/** The line above the list after a save: whether the test on a host worked. */
export function savedBanner(key: string, test: LoginTestResult): Banner {
  const registry = registryOf(key);
  const host = test.host ?? "";
  if (test.state === "ok")
    return {
      title: "Saved and tested.",
      text: `${host} logged in to ${registry} with it. Sending it to every host now.`,
      tone: "ok",
      icon: "check",
      alert: false,
    };
  if (test.state === "no_agent")
    return {
      title: "Saved.",
      text: "No host is online to test it. The first host that connects tests it and gets it.",
      tone: "plain",
      icon: "info",
      alert: false,
    };
  switch (test.failure) {
    case "unreachable":
      return {
        title: "Saved, but not tested.",
        text: `${host} couldn’t reach ${registry}. The login is still sent to every host.`,
        tone: "warn",
        icon: "warning",
        alert: true,
      };
    case "tls":
      return {
        title: "Saved, but the test failed.",
        text: `${host} couldn’t verify the TLS certificate of ${registry}. Hosts won’t be able to pull from it until the certificate is trusted.`,
        tone: "danger",
        icon: "warning",
        alert: true,
      };
    default:
      return {
        title: "Saved, but the test failed.",
        text: `${registry} refused this login when ${host} tried it. Check the username and token, then use Replace.`,
        tone: "danger",
        icon: "warning",
        alert: true,
      };
  }
}

export const deletedBanner: Banner = {
  title: "Login deleted.",
  text: "Hosts drop it at their next update.",
  tone: "plain",
  icon: "check",
  alert: false,
};

export type HostLook = { label: string; detail: string; tone: Tone; busy: boolean; canRetry: boolean };

function seconds(iso: string | undefined): number | undefined {
  if (!iso) return undefined;
  const at = Date.parse(iso);
  return Number.isNaN(at) ? undefined : at / 1000;
}

/** "40 s", or "3 min" from a minute on. */
function waitText(secs: number): string {
  return secs < 60 ? `${secs} s` : `${Math.ceil(secs / 60)} min`;
}

/** One host's row in "On each host", as of now (unix seconds). */
export function hostLook(row: LoginHostStatus, locked: boolean, now: number): HostLook {
  const sending: HostLook = {
    label: "Sending…",
    detail: "The logins changed; this host gets the new set now.",
    tone: "muted",
    busy: true,
    canRetry: false,
  };
  switch (row.state) {
    case "current": {
      const at = seconds(row.at);
      return {
        label: "Has the current logins",
        detail: at === undefined ? "" : `Received ${timeAgo(now - at)}.`,
        tone: "ok",
        busy: false,
        canRetry: false,
      };
    }
    case "sending":
      return sending;
    case "failed": {
      const retry = seconds(row.retry_at);
      const wait = retry === undefined ? 0 : Math.ceil(retry - now);
      return {
        label: "Delivery failed",
        detail: `The agent didn’t confirm the new set.${wait > 0 ? ` Trying again in ${waitText(wait)}.` : ""}`,
        tone: "danger",
        busy: false,
        canRetry: !locked,
      };
    }
    case "offline": {
      const seen = seconds(row.last_seen);
      return {
        label: "Offline",
        detail: `${seen === undefined ? "" : `Last seen ${timeAgo(now - seen)}. `}It gets the current logins when it reconnects.`,
        tone: "muted",
        busy: false,
        canRetry: false,
      };
    }
    default:
      // Online without the current set. While unlocked the master is about
      // to send it; while locked it can't until the store is unlocked.
      if (!locked) return sending;
      return {
        label: "Missing the logins",
        detail: row.set_id
          ? "It gets the current logins when you unlock."
          : "Rebooted while the secret store was locked, so it has none. It gets them when you unlock.",
        tone: "warn",
        busy: false,
        canRetry: false,
      };
  }
}

/** The pill in the "On each host" header. */
export function fleetLabel(rows: readonly LoginHostStatus[]): { text: string; tone: Tone } {
  const total = rows.length;
  const current = rows.filter((row) => row.state === "current").length;
  if (current === total)
    return { text: total === 1 ? "1 host up to date" : `All ${total} hosts up to date`, tone: "ok" };
  return { text: `${current} of ${total} hosts up to date`, tone: "warn" };
}
/**
 * The note on a stack waiting for a registry login, when the master knows
 * the login is saved but not on the host yet; undefined otherwise, and the
 * agent's own reason shows.
 */
export function waitingLoginNote(
  stack: { waiting_login_key?: string; waiting_login_state?: string },
  hostID: string,
): string | undefined {
  if (stack.waiting_login_state !== "not_on_host" || !stack.waiting_login_key) return undefined;
  return `Waiting for a registry login: ${hostID} doesn’t have the login for ${stack.waiting_login_key} yet. The stack starts as soon as it arrives.`;
}
