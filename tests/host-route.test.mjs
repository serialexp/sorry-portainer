import { test } from "node:test";
import assert from "node:assert/strict";
import { editStackPath, hostPath, newStackPath, parseHostPath } from "../src/host-route.ts";

test("host paths preserve the host and exactly one resource section", () => {
  for (const section of ["containers", "images", "volumes", "stacks"]) {
    assert.deepEqual(parseHostPath(hostPath("local-a", section)), { hostID: "local-a", section });
    assert.deepEqual(parseHostPath(hostPath("build node", section)), { hostID: "build node", section });
  }
});

test("new stack is a separate host-scoped route", () => {
  assert.deepEqual(parseHostPath(newStackPath("build node")), { hostID: "build node", section: "stacks", action: "new" });
});

test("editing a stack has its own host-scoped route", () => {
  assert.deepEqual(parseHostPath(editStackPath("local-a", "my_stack")), { hostID: "local-a", section: "stacks", action: "edit", stackName: "my_stack" });
});

test("invalid, incomplete and unrelated paths are not host routes", () => {
  for (const path of ["/", "/local-a", "/local-a/stacks/other", "/local-a/stacks/%2F/edit", "/local-a/images/extra", "/%/images", "/%2F/images", "/../images"]) {
    assert.equal(parseHostPath(path), null, path);
  }
});
