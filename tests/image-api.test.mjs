import { test } from "node:test";
import assert from "node:assert/strict";
import { deleteImage, getImage } from "../src/fleet-api.ts";

const ok = (body) => ({ ok: true, json: async () => body });

test("an image is read from its host by ID", async () => {
  const calls = [];
  const request = async (url) => {
    calls.push(url);
    return ok({ id: "0f1e2d3c4b5a", platform: "linux/amd64", layers: [] });
  };
  const image = await getImage("local-a", "0f1e2d3c4b5a", request);
  assert.equal(image.platform, "linux/amd64");
  assert.deepEqual(calls, ["/api/hosts/local-a/images/0f1e2d3c4b5a"]);
});

test("an image is removed with a DELETE on its ID", async () => {
  const calls = [];
  const request = async (url, init) => {
    calls.push({ url, method: init?.method });
    return { ok: true };
  };
  await deleteImage("local-a", "0f1e2d3c4b5a", request);
  assert.deepEqual(calls, [{ url: "/api/hosts/local-a/images/0f1e2d3c4b5a", method: "DELETE" }]);
});

test("a refused image removal reports the host's reason", async () => {
  const request = async () => ({ ok: false, status: 409, text: async () => "image is used by a container\n" });
  await assert.rejects(deleteImage("local-a", "0f1e2d3c4b5a", request), /image is used by a container/);
});
