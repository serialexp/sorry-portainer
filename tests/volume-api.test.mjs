import { test } from "node:test";
import assert from "node:assert/strict";
import { getVolume } from "../src/fleet-api.ts";

const ok = (body) => ({ ok: true, json: async () => body });
const failed = (status, text = "") => ({ ok: false, status, text: async () => text });

test("a volume is read from its host, with the name escaped", async () => {
  const calls = [];
  const request = async (url) => {
    calls.push(url);
    return ok({ name: "app db", driver: "local", mounts: [] });
  };
  const volume = await getVolume("local a", "app db", request);
  assert.equal(volume.name, "app db");
  assert.deepEqual(calls, ["/api/hosts/local%20a/volumes/app%20db"]);
});

test("a failed read reports the master's message", async () => {
  const request = async () => failed(404, "volume not found");
  await assert.rejects(getVolume("local-a", "missing", request), /volume not found/);
});
