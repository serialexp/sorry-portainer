import { test } from "node:test";
import assert from "node:assert/strict";
import { UnauthorizedError } from "../src/fleet-api.ts";
import {
  checkedText,
  getStackImages,
  imageStatusLine,
  imageTag,
  shortDigest,
  timeAgo,
  updateDetail,
  updatePrompt,
  updateService,
  updatesLabel,
} from "../src/image-updates.ts";
import { StackOperationError } from "../src/stack-api.ts";

const ok = (body) => ({ ok: true, json: async () => body });
const failed = (status, text = "") => ({ ok: false, status, text: async () => text });
const now = 1_800_000_000;
const a = `sha256:${"a".repeat(64)}`;
const b = `sha256:${"b".repeat(64)}`;

test("a page open asks the agent's cache, Check now forces a registry check", async () => {
  const calls = [];
  const request = async (url) => {
    calls.push(url);
    return ok({ name: "homes", services: [] });
  };
  await getStackImages("local a", "homes", false, request);
  await getStackImages("local a", "homes", true, request);
  assert.deepEqual(calls, ["/api/hosts/local%20a/stacks/homes/images", "/api/hosts/local%20a/stacks/homes/images?force=1"]);
  await assert.rejects(getStackImages("h", "s", false, async () => failed(401)), UnauthorizedError);
  await assert.rejects(getStackImages("h", "s", false, async () => failed(502, "host unavailable")), /host unavailable/);
});

test("an update posts the service and reports a failed run with its output", async () => {
  let sent;
  const result = await updateService("h", "homes", "app", async (url, init) => {
    sent = { url, method: init.method, body: JSON.parse(init.body) };
    return ok({ name: "homes", operation: "update", success: true, output: "\x1b[32mpulled\x1b[0m" });
  });
  assert.deepEqual(sent, { url: "/api/hosts/h/stacks/homes/update", method: "POST", body: { service: "app" } });
  assert.equal(result.output, "pulled");
  await assert.rejects(
    updateService("h", "homes", "app", async () => ok({ success: false, output: "unauthorized" })),
    (error) => error instanceof StackOperationError && error.message === "Updating app failed." && error.output === "unauthorized",
  );
  await assert.rejects(updateService("h", "homes", "app", async () => failed(401)), UnauthorizedError);
  await assert.rejects(updateService("h", "homes", "nope", async () => failed(502, `stack homes has no service "nope"`)), /no service/);
});

test("time ago reads in the largest whole unit", () => {
  assert.equal(timeAgo(0), "just now");
  assert.equal(timeAgo(59), "just now");
  assert.equal(timeAgo(-5), "just now");
  assert.equal(timeAgo(12 * 60), "12 min ago");
  assert.equal(timeAgo(3600), "1 hour ago");
  assert.equal(timeAgo(2 * 3600 + 59), "2 hours ago");
  assert.equal(timeAgo(3 * 86400), "3 days ago");
  assert.equal(timeAgo(14 * 86400), "2 weeks ago");
  assert.equal(timeAgo(60 * 86400), "2 months ago");
  assert.equal(timeAgo(365 * 86400), "1 year ago");
});

test("digests and tags shorten as the design shows them", () => {
  assert.equal(shortDigest(a), "sha256:aaaaaaaaaaaa");
  assert.equal(shortDigest(undefined), "");
  assert.equal(imageTag("docker.io/aeolun/homes"), "latest");
  assert.equal(imageTag("docker.io/library/postgres:16"), "16");
  assert.equal(imageTag("localhost:5000/app"), "latest");
  assert.equal(imageTag(`quay.io/x/y@${a}`), "");
});

test("each status has its line", () => {
  const behind = { service: "app", image: "docker.io/aeolun/homes:latest", status: "behind", running_digest: a, latest_digest: b, latest_created: now - 3 * 86400 };
  assert.deepEqual(imageStatusLine(behind, now), {
    text: "Newer latest · built 3 days ago",
    tone: "warn",
    title: "docker.io/aeolun/homes:latest now points to a different image than the one running",
  });
  assert.equal(imageStatusLine({ ...behind, latest_created: undefined }, now).text, "Newer latest");
  assert.equal(imageStatusLine({ ...behind, running_digest: undefined }, now).title, "docker.io/aeolun/homes:latest is not on this host yet");
  assert.deepEqual(imageStatusLine({ service: "db", image: "postgres:16", status: "current" }, now), {
    text: "Up to date",
    tone: "ok",
    title: "Running the image postgres:16 points to now",
  });
  const unknown = imageStatusLine({ service: "x", status: "unknown", error: "dial tcp: i/o timeout" }, now);
  assert.deepEqual([unknown.text, unknown.tone, unknown.title], ["Couldn’t check", "muted", "dial tcp: i/o timeout"]);
  assert.equal(imageStatusLine({ service: "x", status: "pinned" }, now).text, "Pinned by digest");
  assert.equal(imageStatusLine({ service: "x", status: "local", error: "no image" }, now).title, "no image");
  assert.equal(imageStatusLine({ service: "x", status: "unchecked", error: "uses variables" }, now).text, "Not checked");
});

test("the header says when images were checked, or which registry failed", () => {
  const current = { service: "a", status: "current", checked_at: now - 12 * 60 };
  const older = { service: "b", status: "behind", checked_at: now - 20 * 60 };
  assert.equal(checkedText([current, older, { service: "c", status: "pinned" }], now), "Images checked 20 min ago");
  assert.equal(checkedText([{ service: "c", status: "local" }], now), "");
  const down = { service: "d", status: "unknown", registry: "docker.io", checked_at: now - 2 * 3600, error: "timeout" };
  assert.equal(checkedText([current, down], now), "Couldn’t reach docker.io · last checked 2 hours ago");
  assert.equal(checkedText([{ ...down, checked_at: undefined }], now), "Couldn’t reach docker.io");
  assert.equal(checkedText([{ service: "d", status: "unknown" }], now), "Couldn’t reach the registry");
});

test("updates count only services behind", () => {
  assert.equal(updatesLabel([{ status: "current" }]), "");
  assert.equal(updatesLabel([{ status: "behind" }, { status: "unknown" }]), "1 update");
  assert.equal(updatesLabel([{ status: "behind" }, { status: "behind" }]), "2 updates");
});

test("the confirm row follows whether the stack runs", () => {
  assert.deepEqual(updatePrompt("web", "caddy:latest", true), {
    text: "Pull the newest caddy:latest and recreate web. Other services keep running; web is down for a few seconds.",
    label: "Update web",
  });
  assert.deepEqual(updatePrompt("web", "caddy:latest", false), {
    text: "Pull the newest caddy:latest. The stack is stopped, so web uses it the next time the stack starts.",
    label: "Pull image",
  });
  const img = { service: "web", image: "caddy:latest", status: "behind", running_digest: a, latest_digest: b, latest_created: now - 86400 };
  assert.equal(updateDetail(img, now), "running sha256:aaaaaaaaaaaa → latest now sha256:bbbbbbbbbbbb · built 1 day ago");
  assert.equal(updateDetail({ ...img, running_digest: undefined, latest_created: undefined }, now), "not on this host → latest now sha256:bbbbbbbbbbbb");
});
