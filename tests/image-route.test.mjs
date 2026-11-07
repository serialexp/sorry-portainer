import { test } from "node:test";
import assert from "node:assert/strict";
import { imagePath, parseHostPath } from "../src/host-route.ts";

test("an image's page is its host's images section plus the ID", () => {
  assert.equal(imagePath("local-a", "0f1e2d3c4b5a"), "/local-a/images/0f1e2d3c4b5a");
  assert.deepEqual(parseHostPath("/local-a/images/0f1e2d3c4b5a"), {
    hostID: "local-a",
    section: "images",
    action: "view",
    imageID: "0f1e2d3c4b5a",
  });
});

test("image pages take only hexadecimal IDs of podman's lengths", () => {
  for (const bad of ["/local-a/images/abc", "/local-a/images/owned-web", "/local-a/images/0F1E2D3C4B5A", "/local-a/images/..%2F..%2Fx"]) {
    assert.equal(parseHostPath(bad), null, bad);
  }
});
