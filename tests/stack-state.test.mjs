import { test } from "node:test";
import assert from "node:assert/strict";
import { serviceStateLabel, stackRunState } from "../src/stack-state.ts";

const web = { service: "web", container: "app-web-1", state: "running" };
const db = { service: "db", container: "app-db-1", state: "running" };

test("all services running is Running", () => {
  assert.deepEqual(stackRunState({ status: "saved", services: [web, db] }), {
    label: "Running",
    tone: "running",
    total: 2,
    running: 2,
    unhealthy: 0,
  });
});

test("some services running is a partial count", () => {
  const state = stackRunState({ status: "saved", services: [web, { service: "db", state: "absent" }] });
  assert.deepEqual(state, { label: "1 of 2 running", tone: "partial", total: 2, running: 1, unhealthy: 0 });
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
