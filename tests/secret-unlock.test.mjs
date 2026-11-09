import assert from "node:assert/strict";
import { test } from "node:test";
import { retryMessage, retrySecondsLeft, unlockFailure, wrongPassphraseMessage } from "../src/secret-unlock.ts";
import { TooManyAttemptsError, WrongPassphraseError } from "../src/secrets-api.ts";

const now = 1_000_000;

test("a wrong passphrase is the red error line, with no wait", () => {
  assert.deepEqual(unlockFailure(new WrongPassphraseError("wrong passphrase"), now), {
    error: wrongPassphraseMessage,
    retryUntil: 0,
  });
  assert.equal(wrongPassphraseMessage, "Could not unlock: the passphrase is wrong.");
});

test("the attempt limit counts down from the master's wait instead of showing an error", () => {
  assert.deepEqual(unlockFailure(new TooManyAttemptsError("too many", 42), now), {
    error: "",
    retryUntil: now + 42_000,
  });
  assert.deepEqual(unlockFailure(new TooManyAttemptsError("too many; try again", 0), now), {
    error: "too many; try again",
    retryUntil: 0,
  });
});

test("other failures show their own message", () => {
  assert.deepEqual(unlockFailure(new Error("secret store not configured"), now), {
    error: "secret store not configured",
    retryUntil: 0,
  });
  assert.equal(unlockFailure("odd", now).error, "Could not unlock the secret store.");
});

test("seconds left round up and stop at zero", () => {
  assert.equal(retrySecondsLeft(now + 42_000, now), 42);
  assert.equal(retrySecondsLeft(now + 41_001, now), 42);
  assert.equal(retrySecondsLeft(now + 1, now), 1);
  assert.equal(retrySecondsLeft(now, now), 0);
  assert.equal(retrySecondsLeft(now - 5, now), 0);
  assert.equal(retrySecondsLeft(0, now), 0);
  assert.equal(retryMessage(42), "Too many attempts. Try again in 42 s.");
});
