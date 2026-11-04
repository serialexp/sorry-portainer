import { test } from "node:test";
import assert from "node:assert/strict";
import {
  certificateState,
  createToken,
  EnrollmentUnavailableError,
  hostIDPattern,
  listEnrolledHosts,
  listTokens,
  removeHost,
  revokeToken,
} from "../src/enrollment-api.ts";
import { UnauthorizedError } from "../src/fleet-api.ts";

const ok = (body) => ({ ok: true, status: 200, json: async () => body });
const failed = (status, text = "") => ({ ok: false, status, text: async () => text });
const day = 24 * 60 * 60 * 1000;

test("certificate state follows the agent's renewal window", () => {
  const now = new Date("2026-11-05T12:00:00Z");
  const after = (days) => new Date(now.getTime() + days * day).toISOString();
  assert.equal(certificateState({ current_not_after: after(90), pending: false }, now), "valid");
  assert.equal(certificateState({ current_not_after: after(30.01), pending: false }, now), "valid");
  assert.equal(certificateState({ current_not_after: after(29.99), pending: false }, now), "overdue");
  assert.equal(certificateState({ current_not_after: after(29), pending: true }, now), "renewing");
  assert.equal(certificateState({ current_not_after: after(0), pending: true }, now), "expired");
  assert.equal(certificateState({ current_not_after: after(-1), pending: false }, now), "expired");
  assert.equal(certificateState({ current_not_after: "not a date", pending: false }, now), "expired");
});

test("host IDs follow the master's rule", () => {
  for (const id of ["hel1", "jp1.example.com", "a", "A_b-9", "x".repeat(128)]) assert.ok(hostIDPattern.test(id), id);
  for (const id of ["", "-a", ".a", "a b", "a/b", "x".repeat(129), "ä"]) assert.ok(!hostIDPattern.test(id), id);
});

test("listings read hosts and tokens", async () => {
  const calls = [];
  const request = async (url) => {
    calls.push(url);
    return ok([]);
  };
  await listEnrolledHosts(request);
  await listTokens(request);
  assert.deepEqual(calls, ["/api/enrollment/hosts", "/api/enrollment/tokens"]);
});

test("creating a token posts the host and lifetime and is never cached", async () => {
  const calls = [];
  const issued = { token: "spt1.a.b.c", expires_at: "2026-11-05T13:00:00Z", command: "sudo …", reenroll: false };
  const result = await createToken("hel1", "4h", async (url, options) => {
    calls.push({ url, options });
    return ok(issued);
  });
  assert.deepEqual(result, issued);
  assert.equal(calls[0].url, "/api/enrollment/tokens");
  assert.equal(calls[0].options.method, "POST");
  assert.equal(calls[0].options.cache, "no-store");
  assert.deepEqual(JSON.parse(calls[0].options.body), { host_id: "hel1", ttl: "4h" });
});

test("an invalid host ID is refused before any request", async () => {
  let calls = 0;
  await assert.rejects(
    createToken("bad host", "1h", async () => {
      calls++;
      return ok({});
    }),
    /host ID/,
  );
  assert.equal(calls, 0);
});

test("revoke and remove delete by host and escape the ID", async () => {
  const calls = [];
  const request = async (url, options) => {
    calls.push([url, options.method]);
    return { ok: true, status: 204 };
  };
  await revokeToken("hel.1", request);
  await removeHost("jp1_x", request);
  assert.deepEqual(calls, [
    ["/api/enrollment/tokens/hel.1", "DELETE"],
    ["/api/enrollment/hosts/jp1_x", "DELETE"],
  ]);
});

test("failures keep the master's text and are classified", async () => {
  await assert.rejects(listTokens(async () => failed(503, "host enrollment is not configured")), (error) => {
    assert.ok(error instanceof EnrollmentUnavailableError);
    assert.equal(error.message, "host enrollment is not configured");
    return true;
  });
  await assert.rejects(listEnrolledHosts(async () => failed(401, "authentication required")), UnauthorizedError);
  await assert.rejects(revokeToken("hel1", async () => failed(404, "no outstanding token for hel1")), /no outstanding token/);
  await assert.rejects(createToken("hel1", "1h", async () => failed(409)), /Could not create a token \(409\)/);
  await assert.rejects(removeHost("hel1", async () => failed(500)), /Could not remove the host \(500\)/);
});
