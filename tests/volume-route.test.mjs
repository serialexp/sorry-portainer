import { test } from "node:test";
import assert from "node:assert/strict";
import { parseHostPath, volumePath } from "../src/host-route.ts";

test("a volume's page is its host's volumes section plus the name", () => {
  assert.equal(volumePath("local-a", "app_db-data"), "/local-a/volumes/app_db-data");
  assert.deepEqual(parseHostPath("/local-a/volumes/app_db-data"), {
    hostID: "local-a",
    section: "volumes",
    action: "view",
    volumeName: "app_db-data",
  });
});

test("volume names follow podman's rule; path tricks are not volume pages", () => {
  for (const bad of ["/local-a/volumes/.hidden", "/local-a/volumes/..", "/local-a/volumes/-dash", "/local-a/volumes/a%2Fb"]) {
    assert.equal(parseHostPath(bad), null, bad);
  }
  assert.equal(parseHostPath("/local-a/volumes/a/b")?.volumeName, undefined);
});
