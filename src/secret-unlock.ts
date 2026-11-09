// What an unlock attempt shows: the error line, and until when the master
// refuses attempts. Shared by the sidebar panel and the stack page banner,
// which drive the same store and show the same errors and countdown.

import { TooManyAttemptsError, WrongPassphraseError } from "./secrets-api.ts";

export const wrongPassphraseMessage = "Could not unlock: the passphrase is wrong.";

export type UnlockFailure = {
  /** The red error line; empty while counting down. */
  error: string;
  /** Epoch milliseconds until which attempts are refused; 0 for none. */
  retryUntil: number;
};

/** Maps a failed unlock to what the forms show, as of `now` (epoch ms). */
export function unlockFailure(cause: unknown, now: number): UnlockFailure {
  if (cause instanceof WrongPassphraseError) return { error: wrongPassphraseMessage, retryUntil: 0 };
  // A zero wait has nothing to count down, so the master's message shows.
  if (cause instanceof TooManyAttemptsError && cause.retryAfterSeconds > 0)
    return { error: "", retryUntil: now + cause.retryAfterSeconds * 1000 };
  return { error: cause instanceof Error ? cause.message : "Could not unlock the secret store.", retryUntil: 0 };
}

/** Whole seconds left until `retryUntil`, rounded up; 0 once it has passed. */
export function retrySecondsLeft(retryUntil: number, now: number): number {
  return retryUntil > now ? Math.ceil((retryUntil - now) / 1000) : 0;
}

export function retryMessage(seconds: number): string {
  return `Too many attempts. Try again in ${seconds} s.`;
}
