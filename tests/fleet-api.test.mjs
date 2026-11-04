import { test } from "node:test";
import assert from "node:assert/strict";
import {
  containerOperation,
  hostInfo,
  listContainers,
  listHosts,
  listImages,
  listVolumes,
  login,
  logout,
  MasterUnreachableError,
  UnauthorizedError,
} from "../src/fleet-api.ts";

const ok = (body) => ({ ok: true, status: 200, json: async () => body });
const failed = (status, text = "") => ({ ok: false, status, text: async () => text });

test("login posts the password and tells a wrong password from other failures", async () => {
  const calls = [];
  await login("secret", async (url, options) => {
    calls.push({ url, options });
    return { ok: true, status: 204 };
  });
  assert.equal(calls[0].url, "/api/session/login");
  assert.equal(calls[0].options.method, "POST");
  assert.deepEqual(JSON.parse(calls[0].options.body), { password: "secret" });
  await assert.rejects(login("wrong", async () => failed(401, "invalid credentials")), (error) => {
    assert.ok(error instanceof UnauthorizedError);
    assert.equal(error.message, "Wrong password.");
    return true;
  });
  await assert.rejects(
    login("secret", async () => failed(429, "too many failed sign-ins; try again in 3 seconds")),
    (error) => {
      assert.ok(!(error instanceof UnauthorizedError) && !(error instanceof MasterUnreachableError));
      assert.equal(error.message, "too many failed sign-ins; try again in 3 seconds");
      return true;
    },
  );
  await assert.rejects(login("secret", async () => failed(400)), /Could not sign in \(400\)/);
});

test("only a missing master counts as unreachable at sign-in", async () => {
  for (const status of [502, 503, 504])
    await assert.rejects(login("secret", async () => failed(status)), MasterUnreachableError);
  await assert.rejects(
    login("secret", async () => {
      throw new TypeError("fetch failed");
    }),
    MasterUnreachableError,
  );
  await assert.rejects(login("secret", async () => failed(500)), (error) => !(error instanceof MasterUnreachableError));
});

test("logout posts to the session route", async () => {
  const calls = [];
  await logout(async (url, options) => {
    calls.push([url, options.method]);
    return { ok: true, status: 204 };
  });
  assert.deepEqual(calls, [["/api/session/logout", "POST"]]);
  await assert.rejects(logout(async () => failed(500)), /Could not sign out \(500\)/);
});

test("host reads are host-scoped and escape the host ID", async () => {
  const calls = [];
  const request = async (url) => {
    calls.push(url);
    return ok([]);
  };
  await listHosts(request);
  await hostInfo("build node", request);
  await listContainers("build node", request);
  await listVolumes("build node", request);
  await listImages("build node", request);
  assert.deepEqual(calls, [
    "/api/hosts",
    "/api/hosts/build%20node/info",
    "/api/hosts/build%20node/containers",
    "/api/hosts/build%20node/volumes",
    "/api/hosts/build%20node/images",
  ]);
});

test("an ended session is reported as UnauthorizedError; other failures keep the master's text", async () => {
  await assert.rejects(listHosts(async () => failed(401, "authentication required")), UnauthorizedError);
  await assert.rejects(listContainers("host", async () => failed(503, "host unavailable")), /host unavailable/);
  await assert.rejects(listImages("host", async () => failed(504)), /Could not load images \(504\)/);
});

test("container operations post the container ID to start or stop", async () => {
  const calls = [];
  const request = async (url, options) => {
    calls.push({ url, options });
    return { ok: true, status: 200 };
  };
  await containerOperation("build node", "web-1", "stop", request);
  await containerOperation("build node", "web-1", "start", request);
  assert.deepEqual(
    calls.map((call) => [call.url, call.options.method, JSON.parse(call.options.body)]),
    [
      ["/api/hosts/build%20node/stop", "POST", { container_id: "web-1" }],
      ["/api/hosts/build%20node/start", "POST", { container_id: "web-1" }],
    ],
  );
  await assert.rejects(containerOperation("host", "web-1", "start", async () => failed(502)), /Could not start the container \(502\)/);
});
