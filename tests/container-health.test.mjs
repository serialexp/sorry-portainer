import assert from "node:assert/strict";
import { test } from "node:test";
import { attention, formatAgo, healthNotes } from "../src/container-health.ts";

const running = { state: "running" };
const texts = (notes) => notes.map((n) => [n.text, n.warn]);

test("attention ranks a failing check, then a recent restart, then a recovery", () => {
  assert.equal(attention({ ...running, health: "unhealthy", recent_restarts: 3 }), "unhealthy");
  assert.equal(attention({ ...running, health: "healthy", recent_restarts: 1, unhealthy_ago_s: 30 }), "restarted");
  assert.equal(attention({ ...running, health: "healthy", unhealthy_ago_s: 30 }), "recovered");
  assert.equal(attention({ ...running, health: "healthy", restarts: 50 }), undefined);
  assert.equal(attention({ ...running }), undefined);
});

test("health only needs attention while the container runs; restarts always do", () => {
  assert.equal(attention({ state: "exited", health: "unhealthy" }), undefined);
  assert.equal(attention({ state: "exited", health: "healthy", unhealthy_ago_s: 30 }), undefined);
  assert.equal(attention({ state: "exited", recent_restarts: 2 }), "restarted");
});

test("a container still starting after a failure has not recovered", () => {
  assert.equal(attention({ ...running, health: "starting", unhealthy_ago_s: 30 }), undefined);
});

test("formatAgo reads just now under a minute and whole minutes after", () => {
  assert.equal(formatAgo(1), "just now");
  assert.equal(formatAgo(59), "just now");
  assert.equal(formatAgo(60), "1 min ago");
  assert.equal(formatAgo(899), "14 min ago");
});

test("notes show health, then restarts, warning on what needs attention", () => {
  assert.deepEqual(
    texts(healthNotes({ ...running, health: "unhealthy", restarts: 4, recent_restarts: 1, last_restart_ago_s: 240 })),
    [
      ["Unhealthy", true],
      ["Restarted 4 min ago", true],
    ],
  );
  assert.deepEqual(texts(healthNotes({ ...running, health: "healthy", unhealthy_ago_s: 400 })), [
    ["Was unhealthy 6 min ago", true],
  ]);
  assert.deepEqual(
    texts(healthNotes({ ...running, health: "starting", restarts: 3, recent_restarts: 3, last_restart_ago_s: 5 })),
    [
      ["Starting", false],
      ["3 restarts in 15 min", true],
    ],
  );
  assert.deepEqual(texts(healthNotes({ ...running, health: "healthy", restarts: 44 })), [
    ["Healthy", false],
    ["Restarted 44×", false],
  ]);
});

test("a stopped container shows no health, but its restarts", () => {
  assert.deepEqual(texts(healthNotes({ state: "exited", health: "unhealthy", restarts: 2 })), [
    ["Restarted 2×", false],
  ]);
  assert.deepEqual(healthNotes({ state: "exited" }), []);
});
