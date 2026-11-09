import { A, useLocation, useNavigate } from "@solidjs/router";
import { createEffect, createSignal, For, Show } from "solid-js";
import { ContainerChip } from "./container-chip";
import { getVolume, type Volume, type VolumeDetail } from "./fleet-api";
import { filteredVolumes, fleet, removeVolume, sessionEnded } from "./fleet-store";
import { formatBytes } from "./format";
import { hostPath, parseHostPath, stackPath, volumePath } from "./host-route";
import { hostCrumbs, PageHeader } from "./page-header";
import { RemoveButton } from "./remove-button";

/**
 * Why a volume cannot be removed, or undefined when it can. A volume that a container
 * of this prefix mounts names the containers; one that only another prefix mounts says so.
 */
function blockedBy(volume: Volume): string | undefined {
  const count = volume.mounts?.length ?? 0;
  if (count > 0) return `Mounted by ${count} container${count === 1 ? "" : "s"}`;
  if (volume.mounted_outside_prefix) return "Mounted by a container outside this agent's prefix";
  return undefined;
}

/** The button's text while the volume is blocked only by another prefix; undefined keeps "Remove volume". */
function blockedLabel(volume: Volume): string | undefined {
  return (volume.mounts?.length ?? 0) === 0 && volume.mounted_outside_prefix ? "In use elsewhere" : undefined;
}

/** The size as the agent measured it; "At least" marks a total that skipped unreadable files. */
function sizeText(volume: Volume): string {
  if (volume.size_bytes === undefined) return "Not measured yet"; // A non-breaking space keeps the number and its unit on one line in a narrow column.
  return `${volume.size_partial ? "At least " : ""}${formatBytes(volume.size_bytes).replace(" ", String.fromCharCode(160))}`;
}

/** The Compose project that created the volume, from the label Podman Compose sets. */
function composeProject(volume: VolumeDetail): string | undefined {
  const labels = volume.labels ?? {};
  return labels["io.podman.compose.project"] ?? labels["com.docker.compose.project"];
}

/** The volume's name inside its Compose project ("db-data" for "app_db-data"); the full name otherwise. */
function shortVolumeName(volume: VolumeDetail): string {
  const project = composeProject(volume);
  return project && volume.name.startsWith(`${project}_`) ? volume.name.slice(project.length + 1) : volume.name;
}

/** The saved stack whose Compose project created the volume, if any. */
function owningStack(volume: VolumeDetail): string | undefined {
  const project = composeProject(volume);
  return project ? fleet.stacks.find((stack) => stack.project === project)?.name : undefined;
}

/** When the volume was created, in local time; Podman's own text when it does not parse. */
function createdText(created: string | undefined): string {
  if (!created) return "Unknown";
  const date = new Date(created);
  return Number.isNaN(date.getTime()) ? created : date.toLocaleString();
}

/** When the size was measured, in words relative to now. */
function measuredText(measuredAt: string): string {
  const formatter = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
  const seconds = Math.round((Date.parse(measuredAt) - Date.now()) / 1000);
  if (Math.abs(seconds) < 3600) return formatter.format(Math.round(seconds / 60), "minute");
  if (Math.abs(seconds) < 86400) return formatter.format(Math.round(seconds / 3600), "hour");
  return formatter.format(Math.round(seconds / 86400), "day");
}

/** The volumes of the selected host, each with the containers that mount it. */
export function VolumeList() {
  const hostID = () => fleet.selectedHost;
  const inUse = () => filteredVolumes().filter((volume) => (volume.mounts?.length ?? 0) > 0).length;
  return (
    <section class="resource-page panel" aria-label="Volumes">
      <div class="panel-heading">
        <div>
          <h2>All volumes</h2>
        </div>
        <span class="cell-muted">{inUse()} in use</span>
        <span class="resource-count">{filteredVolumes().length}</span>
      </div>
      <Show
        when={filteredVolumes().length > 0}
        fallback={
          <div class="resource-empty">
            {fleet.volumes.length === 0
              ? `No volumes on ${hostID()}. Volumes created on this host will show up here.`
              : "No volumes match this search."}
          </div>
        }
      >
        <div class="data-scroll">
          <div class="data-table cols-volumes">
            <div class="data-head">
              <span>Volume</span>
              <span>Driver</span>
              <span>Mounted in</span>
            </div>
            <For each={filteredVolumes()}>
              {(volume) => (
                <div class="data-row">
                  <div class="cell-name">
                    <span class="resource-icon" aria-hidden="true">
                      ◫
                    </span>
                    <div>
                      <A class="volume-name" href={volumePath(hostID(), volume.name)}>
                        {volume.name}
                      </A>
                      <span class="cell-muted" title={volume.mountpoint}>
                        {volume.mountpoint || "Podman managed"}
                      </span>
                    </div>
                  </div>
                  <span class="chip-code">{volume.driver}</span>
                  <div class="container-chips">
                    <Show
                      when={(volume.mounts?.length ?? 0) > 0}
                      fallback={
                        <span class="cell-muted">
                          {volume.mounted_outside_prefix ? "Mounted outside this prefix" : "Not mounted"}
                        </span>
                      }
                    >
                      <For each={volume.mounts}>
                        {(mount) => (
                          <ContainerChip
                            href={hostPath(hostID(), "containers")}
                            container={mount.container}
                            state={mount.state}
                            detail={mount.destination}
                            title={`${mount.container} mounts this at ${mount.destination}`}
                          />
                        )}
                      </For>
                    </Show>
                  </div>
                </div>
              )}
            </For>
          </div>
        </div>
      </Show>
    </section>
  );
}

/** One volume: where it lives on the host, what it holds and who mounts it. */
export function VolumeDetailPage() {
  const location = useLocation();
  const hostID = () => fleet.selectedHost;
  const navigate = useNavigate();
  const name = () => parseHostPath(location.pathname)?.volumeName ?? "";
  const [volume, setVolume] = createSignal<VolumeDetail | null>(null);
  const [loadError, setLoadError] = createSignal("");
  const [copied, setCopied] = createSignal(false);
  // Answers for an earlier volume are dropped, as in the stack pages.
  let request = 0;

  async function load() {
    const host = hostID();
    const volumeName = name();
    const id = ++request;
    setLoadError("");
    try {
      const current = await getVolume(host, volumeName);
      if (id !== request) return;
      setVolume(current);
    } catch (cause) {
      if (id === request && !sessionEnded(cause))
        setLoadError(cause instanceof Error ? cause.message : "Could not load the volume.");
    }
  }

  createEffect(() => {
    void hostID();
    void name();
    setVolume(null);
    void load();
  });

  const crumbName = () => {
    const current = volume();
    return current ? shortVolumeName(current) : name();
  };

  function copyMountpoint(path: string) {
    navigator.clipboard?.writeText(path).catch(() => {});
    setCopied(true);
    setTimeout(() => setCopied(false), 1600);
  }

  return (
    <>
      <PageHeader
        crumbs={[
          ...hostCrumbs(hostID()),
          { label: "Volumes", href: hostPath(hostID(), "volumes") },
          { label: crumbName() },
        ]}
        title={volume()?.name ?? name()}
        actions={
          <Show when={volume()}>
            {(current) => (
              <RemoveButton
                variant="header"
                label="Remove volume"
                question={`Remove volume ${current().name}? Its files are deleted from ${hostID()} and cannot be recovered.`}
                blocked={blockedBy(current())}
                blockedLabel={blockedLabel(current())}
                onRemove={async () => {
                  await removeVolume(current().name);
                  navigate(hostPath(hostID(), "volumes"));
                }}
              />
            )}
          </Show>
        }
      >
        <Show when={volume()}>
          {(current) => (
            <div class="host-chips">
              <span class={(current().mounts?.length ?? 0) > 0 ? "title-chip" : "title-chip stopped"}>
                {(current().mounts?.length ?? 0) > 0
                  ? "In use"
                  : current().mounted_outside_prefix
                    ? "In use elsewhere"
                    : "Not in use"}
              </span>
              <Show when={owningStack(current())}>
                {(stack) => (
                  <A class="header-chip" href={stackPath(hostID(), stack())}>
                    <span>Stack</span>
                    <strong>{stack()}</strong>
                  </A>
                )}
              </Show>
            </div>
          )}
        </Show>
      </PageHeader>
      <section class="panel resource-page stack-detail volume-detail">
        <Show when={loadError()}>
          <div class="resource-empty">{loadError()}</div>
        </Show>
        <Show when={volume()}>
          {(current) => (
            <div class="stack-detail-grid">
              <div class="stack-detail-block detail-wide">
                <h3>Mountpoint</h3>
                <div class="copy-box">
                  <code>{current().mountpoint || "Podman managed"}</code>
                  <Show when={current().mountpoint}>
                    <button
                      class="copy-button"
                      type="button"
                      aria-label="Copy mountpoint"
                      onClick={() => copyMountpoint(current().mountpoint)}
                    >
                      {copied() ? "Copied" : "Copy path"}
                    </button>
                  </Show>
                </div>
              </div>
              <div class="stack-detail-block">
                <h3>Details</h3>
                <dl class="fact-list">
                  <dt>Driver</dt>
                  <dd>{current().driver}</dd> <dt>Created</dt>
                  <dd>{createdText(current().created)}</dd>
                  <dt>Size on disk</dt>
                  <dd>
                    {sizeText(current())}
                    <Show when={current().size_measured_at}>
                      {(measured) => <span class="cell-muted"> · measured {measuredText(measured())}</span>}
                    </Show>
                  </dd>
                </dl>
              </div>
              <div class="stack-detail-block detail-wide">
                <h3>Mounted by</h3>
                <Show
                  when={(current().mounts?.length ?? 0) > 0}
                  fallback={
                    <p class="stack-note">
                      {current().mounted_outside_prefix
                        ? "No container of this prefix mounts the volume. A container outside this prefix does."
                        : "No container of this prefix mounts the volume."}
                    </p>
                  }
                >
                  <div class="data-scroll">
                    <div class="data-table cols-volume-mounts">
                      <div class="data-head">
                        <span>Container</span>
                        <span>Path inside</span>
                        <span>Mode</span>
                        <span>State</span>
                      </div>
                      <For each={current().mounts}>
                        {(mount) => (
                          <div class="data-row">
                            <A href={hostPath(hostID(), "containers")}>{mount.container}</A>
                            <span class="cell-muted">{mount.destination}</span>
                            <span class="chip-code">{mount.mode}</span>
                            <span class={mount.state === "running" ? "state" : "state stopped"}>
                              {mount.state === "running" ? "Running" : mount.state}
                            </span>
                          </div>
                        )}
                      </For>
                    </div>
                  </div>
                </Show>
              </div>
              <div class="stack-detail-block detail-wide">
                <h3>Labels</h3>
                <Show
                  when={Object.keys(current().labels ?? {}).length > 0}
                  fallback={<p class="stack-note">No labels.</p>}
                >
                  <dl class="fact-list">
                    <For each={Object.entries(current().labels ?? {}).sort(([a], [b]) => a.localeCompare(b))}>
                      {([key, value]) => (
                        <>
                          <dt>{key}</dt>
                          <dd>{value}</dd>
                        </>
                      )}
                    </For>
                  </dl>
                </Show>
              </div>
            </div>
          )}
        </Show>
      </section>
    </>
  );
}
