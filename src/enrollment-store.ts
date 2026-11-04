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
import { fleet, loadHosts, sessionEnded } from "./fleet-store";

// Enrollment page state. The issued token is the only copy of its secret: it
// stays here until the page closes or the admin dismisses it, and is never
// written anywhere else.

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

const [enrollment, setEnrollment] = createStore<EnrollmentState>({
  hosts: [],
  tokens: [],
  loading: false,
  availability: "unknown",
  error: "",
  busy: "",
  issued: null,
});

export { enrollment };

function message(cause: unknown, fallback: string): string {
  return cause instanceof Error ? cause.message : fallback;
}

function failed(cause: unknown, fallback: string) {
  if (sessionEnded(cause)) return;
  if (cause instanceof EnrollmentUnavailableError) setEnrollment("availability", "unavailable");
  setEnrollment("error", message(cause, fallback));
}

export async function loadEnrollment() {
  setEnrollment({ loading: true, error: "" });
  try {
    const [hosts, tokens] = await Promise.all([listEnrolledHosts(), listTokens()]);
    setEnrollment({ hosts, tokens, availability: "available" });
  } catch (cause) {
    failed(cause, "Could not load enrollment.");
  } finally {
    setEnrollment("loading", false);
  }
}

async function action(busy: string, run: () => Promise<void>, fallback: string): Promise<boolean> {
  if (enrollment.busy) return false;
  setEnrollment({ busy, error: "" });
  try {
    await run();
    return true;
  } catch (cause) {
    failed(cause, fallback);
    return false;
  } finally {
    setEnrollment("busy", "");
    if (fleet.phase === "ready") await loadEnrollment();
  }
}

/** Creates a token for hostID and keeps it until dismissIssued. */
export function issueToken(hostID: string, ttl: TokenLifetime) {
  return action(
    "issue",
    async () => {
      setEnrollment("issued", null);
      const issued = await createToken(hostID, ttl);
      setEnrollment("issued", { ...issued, host_id: hostID });
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
    async () => {
      await revokeToken(hostID);
      // The token shown on the page no longer works.
      if (enrollment.issued?.host_id === hostID) setEnrollment("issued", null);
    },
    "Could not revoke the token.",
  );
}

/** Removes an enrolled host; the master disconnects it. */
export function remove(hostID: string) {
  return action(
    `remove:${hostID}`,
    async () => {
      await removeHost(hostID);
      if (enrollment.issued?.host_id === hostID) setEnrollment("issued", null);
      await loadHosts();
    },
    "Could not remove the host.",
  );
}
