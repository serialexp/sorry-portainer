import { test } from "node:test";
import assert from "node:assert/strict";
import { canStart, serviceStateLabel, stackRunState } from "../src/stack-state.ts";

const web = { service: "web", container: "app-web-1", state: "running" };
const db = { service: "db", container: "app-db-1", state: "running" };

test("all services running is Running", () => {
  assert.deepEqual(stackRunState({ status: "saved", services: [web, db] }), {
    label: "Running",
    tone: "running",
    total: 2,
    running: 2,
    unhealthy: 0,
    restarted: 0,
    recovered: 0,
  });
});

test("some services running is a partial count", () => {
  const state = stackRunState({ status: "saved", services: [web, { service: "db", state: "absent" }] });
  assert.deepEqual(state, {
    label: "1 of 2 running",
    tone: "partial",
    total: 2,
    running: 1,
    unhealthy: 0,
    restarted: 0,
    recovered: 0,
  });
});

test("no running service, or no container at all, is Stopped", () => {
  assert.equal(stackRunState({ status: "saved", services: [{ service: "web", state: "exited" }] }).tone, "stopped");
  assert.equal(stackRunState({ status: "saved", services: [{ service: "web", state: "absent" }] }).label, "Stopped");
});

test("a service with several containers counts once, and running if any runs", () => {
  const services = [
    { service: "app", container: "app-1", state: "exited" },
    { service: "app", container: "app-2", state: "running" },
  ];
  assert.deepEqual(stackRunState({ status: "saved", services }), {
    label: "Running",
    tone: "running",
    total: 1,
    running: 1,
    unhealthy: 0,
    restarted: 0,
    recovered: 0,
  });
});

test("all services running with a failing health check is degraded", () => {
  const sick = { ...db, health: "unhealthy" };
  assert.deepEqual(stackRunState({ status: "saved", services: [{ ...web, health: "healthy" }, sick] }), {
    label: "Running, 1 unhealthy",
    tone: "degraded",
    total: 2,
    running: 2,
    unhealthy: 1,
    restarted: 0,
    recovered: 0,
  });
});

test("starting and healthy services keep a stack Running", () => {
  const services = [
    { ...web, health: "starting" },
    { ...db, health: "healthy" },
  ];
  assert.equal(stackRunState({ status: "saved", services }).tone, "running");
});

test("unhealthy counts only running containers, once per service", () => {
  const services = [
    { service: "app", container: "app-1", state: "running", health: "unhealthy" },
    { service: "app", container: "app-2", state: "running", health: "unhealthy" },
    { service: "db", container: "db-1", state: "exited", health: "unhealthy" },
  ];
  assert.deepEqual(stackRunState({ status: "saved", services }), {
    label: "1 of 2 running, 1 unhealthy",
    tone: "partial",
    total: 2,
    running: 1,
    unhealthy: 1,
    restarted: 0,
    recovered: 0,
  });
});

test("unknown runtime state, invalid compose and no services are reported as such", () => {
  assert.equal(stackRunState({ status: "saved", services: [{ service: "web", state: "unknown" }] }).tone, "unknown");
  assert.equal(stackRunState({ status: "invalid" }).tone, "invalid");
  assert.equal(stackRunState({ status: "saved", services: [] }).label, "No services");
  assert.equal(stackRunState({ status: "saved" }).label, "No services");
});

test("service state labels are capitalized, absent reads Not created", () => {
  assert.equal(serviceStateLabel("running"), "Running");
  assert.equal(serviceStateLabel("exited"), "Exited");
  assert.equal(serviceStateLabel("unknown"), "Unknown");
  assert.equal(serviceStateLabel("absent"), "Not created");
  assert.equal(serviceStateLabel(""), "Unknown");
});

test("a recent restart or recovered health check degrades a running stack", () => {
  const services = [
    { ...web, recent_restarts: 2, restarts: 9, last_restart_ago_s: 30 },
    { ...db, health: "healthy", unhealthy_ago_s: 400 },
    { service: "cache", container: "app-cache-1", state: "running", restarts: 4 },
  ];
  assert.deepEqual(stackRunState({ status: "saved", services }), {
    label: "Running, 1 restarted, 1 recovered",
    tone: "degraded",
    total: 3,
    running: 3,
    unhealthy: 0,
    restarted: 1,
    recovered: 1,
  });
});

test("a service counts once, under its most urgent reason", () => {
  const services = [
    { service: "app", container: "app-1", state: "running", health: "unhealthy", recent_restarts: 1 },
    { service: "app", container: "app-2", state: "running", health: "healthy", unhealthy_ago_s: 60 },
  ];
  const state = stackRunState({ status: "saved", services });
  assert.equal(state.label, "Running, 1 unhealthy");
  assert.deepEqual([state.unhealthy, state.restarted, state.recovered], [1, 0, 0]);
});

test("a crash loop caught between runs still warns", () => {
  const services = [{ service: "app", container: "app-1", state: "exited", restarts: 40, recent_restarts: 12 }];
  assert.deepEqual(stackRunState({ status: "saved", services }), {
    label: "Stopped, 1 restarted",
    tone: "partial",
    total: 1,
    running: 0,
    unhealthy: 0,
    restarted: 1,
    recovered: 0,
  });
});

test("Start is offered unless every service already runs", () => {
  const start = (services, status = "saved") => canStart(stackRunState({ status, services }));
  assert.equal(start([web, db]), false);
  assert.equal(start([web, { ...db, health: "unhealthy" }]), false);
  assert.equal(start([web, { service: "db", state: "absent" }]), true);
  assert.equal(start([{ service: "web", state: "exited", recent_restarts: 3 }]), true);
  assert.equal(start([{ service: "web", state: "absent" }]), true);
  assert.equal(start([{ service: "web", state: "unknown" }]), true);
  assert.equal(start([]), true);
  assert.equal(start(undefined, "invalid"), true);
});
