import { test } from "node:test";
import assert from "node:assert/strict";
import { parseHostPath, stackPath } from "../src/host-route.ts";

test("a stack's own page is its host's stacks section plus the name", () => {
  assert.equal(stackPath("local-a", "web"), "/local-a/stacks/web");
  assert.deepEqual(parseHostPath("/local-a/stacks/web"), {
    hostID: "local-a",
    section: "stacks",
    action: "view",
    stackName: "web",
  });
});

test("new and edit keep their own routes; 'new' is never a stack page", () => {
  assert.deepEqual(parseHostPath("/local-a/stacks/new"), { hostID: "local-a", section: "stacks", action: "new" });
  assert.deepEqual(parseHostPath("/local-a/stacks/web/edit"), {
    hostID: "local-a",
    section: "stacks",
    action: "edit",
    stackName: "web",
  });
});

test("a stack page refuses a name the create form would not accept", () => {
  assert.equal(parseHostPath("/local-a/stacks/Web"), null);
  assert.equal(parseHostPath("/local-a/stacks/..%2Fx"), null);
  assert.equal(parseHostPath("/local-a/stacks/bad%20name"), null);
});
