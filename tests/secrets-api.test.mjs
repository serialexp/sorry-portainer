import { test } from "node:test";
import assert from "node:assert/strict";
import {
  SecretsUnavailableError,
  deleteStackSecret,
  getStoreState,
  initializeStore,
  listStackSecrets,
  lockStore,
  putStackSecret,
  unlockStore,
} from "../src/secrets-api.ts";

const ok = (body) => ({ ok: true, json: async () => body });
const fail = (status, text) => ({ ok: false, status, text: async () => text });

test("store state routes send the passphrase only in the body", async () => {
  const calls = [];
  const request = async (url, options) => {
    calls.push({ url, options });
    return ok({ state: "unlocked" });
  };
  assert.equal(await getStoreState(request), "unlocked");
  assert.equal(await initializeStore("correct horse battery", request), "unlocked");
  assert.equal(await unlockStore("correct horse battery", request), "unlocked");
  assert.equal(await lockStore(request), "unlocked");
  assert.deepEqual(
    calls.map((call) => call.url),
    ["/api/secrets/status", "/api/secrets/initialize", "/api/secrets/unlock", "/api/secrets/lock"],
  );
  assert.deepEqual(JSON.parse(calls[1].options.body), { passphrase: "correct horse battery" });
  assert.equal(calls[3].options.method, "POST");
});

test("short passphrases are refused before any request", async () => {
  let calls = 0;
  await assert.rejects(
    initializeStore("too short", async () => {
      calls++;
      return ok({ state: "unlocked" });
    }),
    /at least 12/,
  );
  assert.equal(calls, 0);
});

test("server errors carry the server's message; 503 means no secret store", async () => {
  await assert.rejects(unlockStore("wrong passphrase!", async () => fail(403, "wrong passphrase")), /wrong passphrase/);
  await assert.rejects(getStoreState(async () => fail(503, "secret store not configured")), SecretsUnavailableError);
  await assert.rejects(
    listStackSecrets("h", "s", async () => fail(503, "")),
    (error) => error instanceof SecretsUnavailableError,
  );
});

test("stack secret routes are host- and stack-scoped and never read values back", async () => {
  const calls = [];
  const delivery = { state: "delivered", at: "2026-09-24T00:00:00Z" };
  const request = async (url, options) => {
    calls.push({ url, options });
    if (!options) return ok({ state: "locked", secrets: [{ name: "db", size: 3, updated_at: "x" }], delivery: null });
    return ok({ delivery });
  };
  const listed = await listStackSecrets("build node", "web", request);
  assert.equal(listed.secrets[0].name, "db");
  assert.equal("value" in listed.secrets[0], false);
  assert.deepEqual(await putStackSecret("build node", "web", "db.password", "s3cret\n", request), delivery);
  assert.deepEqual(await deleteStackSecret("build node", "web", "db.password", request), delivery);
  assert.deepEqual(
    calls.map((call) => [call.url, call.options?.method]),
    [
      ["/api/hosts/build%20node/stacks/web/secrets", undefined],
      ["/api/hosts/build%20node/stacks/web/secrets/db.password", "PUT"],
      ["/api/hosts/build%20node/stacks/web/secrets/db.password", "DELETE"],
    ],
  );
  assert.deepEqual(JSON.parse(calls[1].options.body), { value: "s3cret\n" });
});

test("invalid names, empty and oversized values are refused before any request", async () => {
  let calls = 0;
  const request = async () => {
    calls++;
    return ok({ delivery: {} });
  };
  await assert.rejects(putStackSecret("h", "s", "../x", "v", request), /Invalid secret name/);
  await assert.rejects(putStackSecret("h", "s", ".hidden", "v", request), /Invalid secret name/);
  await assert.rejects(putStackSecret("h", "s", "db", "", request), /Enter a secret value/);
  await assert.rejects(putStackSecret("h", "s", "db", "é".repeat(32 * 1024 + 1), request), /64 KiB/);
  assert.equal(calls, 0);
  await putStackSecret("h", "s", "db", "x".repeat(64 * 1024), request);
  assert.equal(calls, 1);
});
