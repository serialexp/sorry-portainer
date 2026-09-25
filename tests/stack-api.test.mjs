import { test } from "node:test";
import assert from "node:assert/strict";
import { createStack, deployStack, getStack, listStacks, updateStack } from "../src/stack-api.ts";

const ok = (body) => ({ ok: true, json: async () => body });

test("listing and detail are host-scoped", async () => {
  const calls = [];
  const request = async (url) => { calls.push(url); return ok([{ name: "web", version: 1 }]); };
  assert.equal((await listStacks("build node", request))[0].name, "web");
  assert.equal((await getStack("build node", "web", request))[0].version, 1);
  assert.deepEqual(calls, ["/api/hosts/build%20node/stacks", "/api/hosts/build%20node/stacks/web"]);
  await assert.rejects(listStacks("host", async () => ({ ok: false, status: 502 })), /502/);
});

test("creation sends no expected version; update sends the version being edited", async () => {
  const calls = [];
  const request = async (url, options) => { calls.push({ url, options }); return ok({}); };
  await createStack("host", "web", "services: {}", request);
  await updateStack("host", "web", "services: {db: {image: postgres}}", 3, request);
  assert.equal(calls.length, 2);
  assert.equal(calls[0].options.method, "POST");
  assert.deepEqual(JSON.parse(calls[0].options.body), { name: "web", compose_yaml: "services: {}" });
  assert.deepEqual(JSON.parse(calls[1].options.body), { name: "web", compose_yaml: "services: {db: {image: postgres}}", expected_version: 3 });
});

test("deploy invokes up and rejects non-success operations and transport errors", async () => {
  const calls = [];
  const request = async (url, options) => { calls.push({ url, options }); return ok({ success: true, operation: "up" }); };
  await deployStack("build node", "web", request);
  assert.deepEqual(calls, [{ url: "/api/hosts/build%20node/stacks/web/up", options: { method: "POST" } }]);
  await assert.rejects(deployStack("host", "web", async () => ok({ success: false, output: "pull failed" })), /pull failed/);
  await assert.rejects(deployStack("host", "web", async () => ({ ok: false, status: 502, text: async () => "host unavailable" })), /host unavailable/);
});

test("invalid input and failed saves cannot report creation or version update", async () => {
  let calls = 0;
  const request = async () => { calls++; return ok({}); };
  await assert.rejects(createStack("host", "Bad Name", "services: {}", request), /Invalid/);
  await assert.rejects(createStack("host", "web", "   ", request), /Enter/);
  assert.throws(() => updateStack("host", "web", "services: {}", 0, request), /version/);
  assert.equal(calls, 0);
  await assert.rejects(createStack("host", "web", "bad", async () => ({ ok: false, status: 409, text: async () => "stack already exists" })), /already exists/);
  await assert.rejects(updateStack("host", "web", "bad", 1, async () => ({ ok: false, status: 409, text: async () => "version mismatch" })), /version mismatch/);
});
