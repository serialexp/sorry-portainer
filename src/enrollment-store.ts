import { createSignal } from "solid-js";
import { createStore } from "solid-js/store";
import {
  createToken,
  type EnrolledHost,
  EnrollmentUnavailableError,
  type IssuedToken,
  listEnrolledHosts,
  listTokens,
  type OutstandingToken,
  removeHost,
  revokeToken,
  type TokenLifetime,
} from "./enrollment-api";
import { fleet, loadHosts, onSessionEnd, sessionEnded } from "./fleet-store";

// Enrollment page state. The issued token is the only copy of its secret: it
// stays here until the page closes, the session ends or the admin dismisses
// it, and is never written anywhere else.

type EnrollmentState = {
  hosts: EnrolledHost[];
  tokens: OutstandingToken[];
  loading: boolean;
  /** "unavailable" when the master has no enrollment service. */
  availability: "unknown" | "available" | "unavailable";
  error: string;
  /** What is in progress: "issue", "revoke:<host>" or "remove:<host>". */
  busy: string;
  issued: (IssuedToken & { host_id: string }) | null;
};

const initial = (): EnrollmentState => ({
  hosts: [],
  tokens: [],
  loading: false,
  availability: "unknown",
  error: "",
  busy: "",
  issued: null,
});

const [enrollment, setEnrollment] = createStore<EnrollmentState>(initial());

export { enrollment };

// The sidebar's count of unused tokens. It lives apart from the page state,
// which resets when the page closes. Each token drops out when it expires;
// one an agent used drops out at the next load.
const [tokenExpiries, setTokenExpiries] = createSignal<number[]>([]);
let expiryTimer: ReturnType<typeof setTimeout> | undefined;

/** How many tokens are issued and not yet used or expired. */
export const outstandingTokenCount = () => tokenExpiries().length;

function trackTokens(expiries: number[]) {
  clearTimeout(expiryTimer);
  const now = Date.now();
  const left = expiries.filter((at) => at > now);
  setTokenExpiries(left);
  if (left.length) expiryTimer = setTimeout(() => trackTokens(tokenExpiries()), Math.min(...left) - now);
}

function countTokens(tokens: OutstandingToken[]) {
  trackTokens(tokens.map((token) => Date.parse(token.expires_at)).filter((at) => !Number.isNaN(at)));
}

/** Loads the token count for the sidebar. A failure leaves the last count; the page shows its own errors. */
export async function loadTokenCount() {
  try {
    countTokens(await listTokens());
  } catch (cause) {
    sessionEnded(cause);
  }
}

onSessionEnd(() => trackTokens([]));

// Bumped by resetEnrollment, so answers to requests made before it, above
// all a token being issued, are dropped. Loads also drop older loads.
let generation = 0;
let loadRequest = 0;

/** Forgets everything, including an issued token; for leaving the page and session ends. */
export function resetEnrollment() {
  ++generation;
  ++loadRequest;
  setEnrollment(initial());
}

onSessionEnd(resetEnrollment);

function message(cause: unknown, fallback: string): string {
  return cause instanceof Error ? cause.message : fallback;
}

function failed(cause: unknown, fallback: string) {
  if (sessionEnded(cause)) return;
  if (cause instanceof EnrollmentUnavailableError) setEnrollment("availability", "unavailable");
  setEnrollment("error", message(cause, fallback));
}

export async function loadEnrollment() {
  const request = ++loadRequest;
  setEnrollment({ loading: true, error: "" });
  try {
    const [hosts, tokens] = await Promise.all([listEnrolledHosts(), listTokens()]);
    if (request === loadRequest) {
      setEnrollment({ hosts, tokens, availability: "available" });
      countTokens(tokens);
    }
  } catch (cause) {
    if (request === loadRequest) failed(cause, "Could not load enrollment.");
  } finally {
    if (request === loadRequest) setEnrollment("loading", false);
  }
}

/**
 * Runs one change at a time. run gets current, which turns false once the
 * store has been reset; nothing is recorded from a request made before that.
 */
async function action(
  busy: string,
  run: (current: () => boolean) => Promise<void>,
  fallback: string,
): Promise<boolean> {
  if (enrollment.busy) return false;
  const started = generation;
  const current = () => started === generation;
  setEnrollment({ busy, error: "" });
  try {
    await run(current);
    return current();
  } catch (cause) {
    if (current()) failed(cause, fallback);
    return false;
  } finally {
    if (current()) {
      setEnrollment("busy", "");
      if (fleet.phase === "ready") await loadEnrollment();
    }
  }
}

/** Creates a token for hostID and keeps it until dismissIssued or a reset. */
export function issueToken(hostID: string, ttl: TokenLifetime) {
  return action(
    "issue",
    async (current) => {
      setEnrollment("issued", null);
      const issued = await createToken(hostID, ttl);
      if (current()) setEnrollment("issued", { ...issued, host_id: hostID });
    },
    "Could not create a token.",
  );
}

export function dismissIssued() {
  setEnrollment("issued", null);
}

export function revoke(hostID: string) {
  return action(
    `revoke:${hostID}`,
    async (current) => {
      await revokeToken(hostID);
      // The token shown on the page no longer works.
      if (current() && enrollment.issued?.host_id === hostID) setEnrollment("issued", null);
    },
    "Could not revoke the token.",
  );
}

/** Removes an enrolled host; the master disconnects it. */
export function remove(hostID: string) {
  return action(
    `remove:${hostID}`,
    async (current) => {
      await removeHost(hostID);
      if (current() && enrollment.issued?.host_id === hostID) setEnrollment("issued", null);
      await loadHosts();
    },
    "Could not remove the host.",
  );
}
