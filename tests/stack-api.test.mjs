import { test } from "node:test";
import assert from "node:assert/strict";
import { UnauthorizedError } from "../src/fleet-api.ts";
import {
  createStack,
  deployStack,
  formatEnvironment,
  getStack,
  listStacks,
  maxEnvironment,
  parseEnvironment,
  rebootLabel,
  StackOperationError,
  stopStack,
  terminalText,
  updateStack,
} from "../src/stack-api.ts";

const ok = (body) => ({ ok: true, json: async () => body });
const failed = (status, text = "") => ({ ok: false, status, text: async () => text });

test("reboot label follows the stack's desired state", () => {
  assert.equal(rebootLabel({ desired: "up" }), "starts after reboot");
  assert.equal(rebootLabel({ desired: "down" }), "stays stopped after reboot");
  assert.equal(rebootLabel({}), "stays stopped after reboot");
});

test("listing and detail are host-scoped", async () => {
  const calls = [];
  const request = async (url) => { calls.push(url); return ok([{ name: "web", version: 1 }]); };
  assert.equal((await listStacks("build node", request))[0].name, "web");
  assert.equal((await getStack("build node", "web", request))[0].version, 1);
  assert.deepEqual(calls, ["/api/hosts/build%20node/stacks", "/api/hosts/build%20node/stacks/web"]);
  await assert.rejects(listStacks("host", async () => failed(502)), /502/);
});

test("a 401 from any stack route means the session ended", async () => {
  const unauthorized = async () => failed(401, "authentication required");
  await assert.rejects(listStacks("host", unauthorized), UnauthorizedError);
  await assert.rejects(getStack("host", "web", unauthorized), UnauthorizedError);
  await assert.rejects(createStack("host", "web", "services: {}", undefined, unauthorized), UnauthorizedError);
  await assert.rejects(deployStack("host", "web", unauthorized), UnauthorizedError);
  await assert.rejects(stopStack("host", "web", unauthorized), UnauthorizedError);
  await assert.rejects(listStacks("host", async () => failed(502)), (error) => !(error instanceof UnauthorizedError));
});

test("creation sends no expected version; update sends the version being edited", async () => {
  const calls = [];
  const request = async (url, options) => { calls.push({ url, options }); return ok({}); };
  await createStack("host", "web", "services: {}", undefined, request);
  await updateStack("host", "web", "services: {db: {image: postgres}}", 3, undefined, request);
  assert.equal(calls.length, 2);
  assert.equal(calls[0].options.method, "POST");
  assert.deepEqual(JSON.parse(calls[0].options.body), { name: "web", compose_yaml: "services: {}" });
  assert.deepEqual(JSON.parse(calls[1].options.body), { name: "web", compose_yaml: "services: {db: {image: postgres}}", expected_version: 3 });
});

test("an environment is sent only when given, and an empty one clears", async () => {
  const bodies = [];
  const request = async (url, options) => { bodies.push(JSON.parse(options.body)); return ok({ name: "web", version: 4 }); };
  await createStack("host", "web", "services: {}", { TZ: "UTC" }, request);
  await updateStack("host", "web", "services: {}", 3, {}, request);
  await updateStack("host", "web", "services: {}", 3, undefined, request);
  assert.deepEqual(bodies[0].environment, { TZ: "UTC" });
  assert.deepEqual(bodies[1].environment, {});
  assert.equal("environment" in bodies[2], false);
});

test("a save answers with the stack and the up it ran", async () => {
  const answer = { name: "web", version: 4, operation: { name: "web", operation: "up", success: false, output: "pull failed" } };
  const saved = await updateStack("host", "web", "services: {}", 3, { A: "1" }, async () => ok(answer));
  assert.equal(saved.version, 4);
  assert.equal(saved.operation.success, false);
});

test("environment text parses like the CLI's env file", () => {
  assert.deepEqual(parseEnvironment("# comment\n\nB=x=y\r\nA=\n  # indented\nC= spaced \n"), { A: "", B: "x=y", C: " spaced " });
  assert.throws(() => parseEnvironment("A=1\nnot a line"), /line 2: expected KEY=VALUE/);
  assert.throws(() => parseEnvironment("1A=x"), /line 1: "1A" is not a valid variable name/);
  assert.throws(() => parseEnvironment("A=1\nA=2"), /line 2: A is set twice/);
  assert.throws(() => parseEnvironment("A=x\0y"), /NUL/);
  const many = Array.from({ length: maxEnvironment + 1 }, (_, i) => `K${i}=v`).join("\n");
  assert.throws(() => parseEnvironment(many), /at most/);
  assert.deepEqual(parseEnvironment(""), {});
});

test("environment text round-trips, sorted by key", () => {
  const environment = { TZ: "Europe/Amsterdam", A_URL: "http://x/?a=b", EMPTY: "" };
  const text = formatEnvironment(environment);
  assert.equal(text, "A_URL=http://x/?a=b\nEMPTY=\nTZ=Europe/Amsterdam");
  assert.deepEqual(parseEnvironment(text), environment);
  assert.equal(formatEnvironment(undefined), "");
});

test("deploy invokes up and rejects non-success operations and transport errors", async () => {
  const calls = [];
  const request = async (url, options) => { calls.push({ url, options }); return ok({ success: true, operation: "up" }); };
  await deployStack("build node", "web", request);
  assert.deepEqual(calls, [{ url: "/api/hosts/build%20node/stacks/web/up", options: { method: "POST" } }]);
  await assert.rejects(deployStack("host", "web", async () => ok({ success: false, output: "pull failed" })), (error) => {
    assert.ok(error instanceof StackOperationError);
    assert.equal(error.message, "Start failed.");
    assert.equal(error.output, "pull failed");
    return true;
  });
  await assert.rejects(deployStack("host", "web", async () => ({ ok: false, status: 502, text: async () => "host unavailable" })), /host unavailable/);
});

test("stop posts down and reports failures", async () => {
  const calls = [];
  const request = async (url, options) => { calls.push({ url, options }); return ok({ success: true, operation: "down" }); };
  await stopStack("build node", "web", request);
  assert.deepEqual(calls, [{ url: "/api/hosts/build%20node/stacks/web/down", options: { method: "POST" } }]);
  await assert.rejects(stopStack("host", "web", async () => ok({ success: false })), /Stop failed\./);
  await assert.rejects(stopStack("host", "web", async () => ({ ok: false, status: 504, text: async () => "" })), /Stop failed \(504\)/);
});

test("invalid input and failed saves cannot report creation or version update", async () => {
  let calls = 0;
  const request = async () => { calls++; return ok({}); };
  await assert.rejects(createStack("host", "Bad Name", "services: {}", undefined, request), /Invalid/);
  await assert.rejects(createStack("host", "web", "   ", undefined, request), /Enter/);
  assert.throws(() => updateStack("host", "web", "services: {}", 0, undefined, request), /version/);
  assert.equal(calls, 0);
  await assert.rejects(createStack("host", "web", "bad", undefined, async () => ({ ok: false, status: 409, text: async () => "stack already exists" })), /already exists/);
  await assert.rejects(updateStack("host", "web", "bad", 1, undefined, async () => ({ ok: false, status: 409, text: async () => "version mismatch" })), /version mismatch/);
});

test("terminal colour codes and titles are stripped from command output", () => {
  assert.equal(terminalText("\x1b[4m>>>> compose <<<<\x1b[0m done"), ">>>> compose <<<< done");
  assert.equal(terminalText("\x1b[1;31mred\x1b[m \x1b]0;title\x07plain"), "red plain");
  assert.equal(terminalText("no codes, 100% [ok]"), "no codes, 100% [ok]");
});

test("a start's output comes back without colour codes", async () => {
  const result = await deployStack("host", "web", async () => ok({ success: true, output: "\x1b[32mstarted\x1b[0m" }));
  assert.equal(result.output, "started");
  await assert.rejects(
    deployStack("host", "web", async () => ok({ success: false, output: "\x1b[31mport in use\x1b[0m" })),
    (error) => error.output === "port in use",
  );
});
