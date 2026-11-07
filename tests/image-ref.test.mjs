import { test } from "node:test";
import assert from "node:assert/strict";
import { splitImageRef } from "../src/image-ref.ts";

test("a reference splits into repository path, name and tag", () => {
  assert.deepEqual(splitImageRef("docker.io/library/caddy:latest"), {
    path: "docker.io/library",
    name: "caddy",
    tag: "latest",
  });
});

test("a registry port is not taken for a tag", () => {
  assert.deepEqual(splitImageRef("registry.local:5000/team/app:1.2"), {
    path: "registry.local:5000/team",
    name: "app",
    tag: "1.2",
  });
  assert.deepEqual(splitImageRef("registry.local:5000/app"), { path: "registry.local:5000", name: "app", tag: "" });
});

test("an untagged reference has no path and no tag", () => {
  assert.deepEqual(splitImageRef("busybox"), { path: "", name: "busybox", tag: "" });
  assert.deepEqual(splitImageRef(""), { path: "", name: "", tag: "" });
});
