import { A, useLocation } from "@solidjs/router";
import { createEffect, createSignal, For, Show } from "solid-js";
import { getVolume, type VolumeDetail, type VolumeMount } from "./fleet-api";
import { filteredVolumes, fleet, sessionEnded } from "./fleet-store";
import { hostPath, parseHostPath, volumePath } from "./host-route";

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
                  <div class="volume-mounts">
                    <Show
                      when={(volume.mounts?.length ?? 0) > 0}
                      fallback={<span class="cell-muted">Not mounted</span>}
                    >
                      <For each={volume.mounts}>{(mount) => <MountChip hostID={hostID()} mount={mount} />}</For>
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

/** One container's mount, as a link to its host's containers and the path it sees. */
function MountChip(props: { hostID: string; mount: VolumeMount }) {
  return (
    <span class="volume-mount" title={`${props.mount.container} mounts this at ${props.mount.destination}`}>
      <span class={`volume-dot ${props.mount.state === "running" ? "up" : "down"}`} aria-hidden="true" />
      <A href={hostPath(props.hostID, "containers")}>{props.mount.container}</A>
      <span class="volume-target">→ {props.mount.destination}</span>
    </span>
  );
}

/** One volume: where it lives on the host, what it holds and who mounts it. */
export function VolumeDetailPage() {
  const location = useLocation();
  const hostID = () => fleet.selectedHost;
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

  function copyMountpoint(path: string) {
    navigator.clipboard?.writeText(path).catch(() => {});
    setCopied(true);
    setTimeout(() => setCopied(false), 1600);
  }

  return (
    <section class="panel resource-page stack-detail volume-detail">
      <div class="stack-detail-head">
        <div class="stack-detail-title">
          <A class="stack-detail-back" href={hostPath(hostID(), "volumes")}>
            ← All volumes
          </A>
          <h2 class="volume-title">{volume()?.name ?? name()}</h2>
          <Show when={volume()}>
            {(current) => (
              <>
                <span class="stack-card-meta">
                  {current().driver} volume on {hostID()}
                </span>
                <div class="stack-detail-chips">
                  <span class={(current().mounts?.length ?? 0) > 0 ? "state" : "state stopped"}>
                    {(current().mounts?.length ?? 0) > 0 ? "In use" : "Not in use"}
                  </span>
                </div>
              </>
            )}
          </Show>
        </div>
      </div>
      <Show when={loadError()}>
        <div class="resource-empty">{loadError()}</div>
      </Show>
      <Show when={volume()}>
        {(current) => (
          <div class="stack-detail-grid">
            <div class="stack-detail-block volume-wide">
              {" "}
              <h3>Mountpoint</h3>
              <div class="volume-path">
                <code>{current().mountpoint || "Podman managed"}</code>
                <Show when={current().mountpoint}>
                  <button
                    class="volume-copy"
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
              <dl class="volume-facts">
                <dt>Driver</dt>
                <dd>{current().driver}</dd>
                <dt>Created</dt>
                <dd>{current().created || "Unknown"}</dd>
              </dl>
            </div>
            <div class="stack-detail-block volume-wide">
              <h3>Mounted by</h3>
              <Show
                when={(current().mounts?.length ?? 0) > 0}
                fallback={<p class="stack-note">No container of this prefix mounts the volume.</p>}
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
            <div class="stack-detail-block volume-wide">
              <h3>Labels</h3>
              <Show
                when={Object.keys(current().labels ?? {}).length > 0}
                fallback={<p class="stack-note">No labels.</p>}
              >
                <dl class="volume-facts">
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
  );
}
