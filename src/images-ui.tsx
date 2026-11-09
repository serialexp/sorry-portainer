import { A, useLocation, useNavigate } from "@solidjs/router";
import { createEffect, createSignal, For, Show } from "solid-js";
import { ContainerChip } from "./container-chip";
import { formatBytes } from "./format";
import { type Image, type ImageDetail, getImage } from "./fleet-api";
import { filteredImages, fleet, removeImage, sessionEnded } from "./fleet-store";
import { hostPath, imagePath, parseHostPath } from "./host-route";
import { splitImageRef } from "./image-ref";
import { hostCrumbs, PageHeader } from "./page-header";
import { RemoveButton } from "./remove-button";

/**
 * Why an image cannot be removed, or undefined when it can. An image that a container
 * of this prefix runs names the containers; one that only another prefix runs says so.
 */
function blockedBy(image: Image): string | undefined {
  const uses = image.containers ?? [];
  if (uses.length > 0) return `In use by ${uses.map((use) => use.container).join(", ")}`;
  if (image.used_outside_prefix) return "In use by a container outside this agent's prefix";
  return undefined;
}

/** The button's text while the image is blocked only by another prefix; undefined keeps "Remove". */
function blockedLabel(image: Image): string | undefined {
  return (image.containers?.length ?? 0) === 0 && image.used_outside_prefix ? "In use elsewhere" : undefined;
}

/** The image's first tag, or its short ID when it has none. */
function imageName(image: Image): string {
  return image.tags?.[0] ?? image.id.slice(0, 12);
}

/** The confirmation for a removal: every tag goes, so a multi-tag image says how many. */
function removalQuestion(image: Image, hostID: string): string {
  const tags = (image.tags ?? []).filter((tag) => tag !== "<none>:<none>");
  const what =
    tags.length > 1
      ? `Remove all ${tags.length} tags of image ${imageName(image)}`
      : `Remove image ${imageName(image)}`;
  return `${what}? Its layers are deleted from ${hostID} and must be pulled again to use it.`;
}

/** The images of the selected host and the containers of this agent that run them. */
export function ImageList() {
  const hostID = () => fleet.selectedHost;
  const total = () => filteredImages().reduce((sum, image) => sum + image.size, 0);
  return (
    <section class="resource-page panel" aria-label="Images">
      <div class="panel-heading">
        <div>
          <h2>All images</h2>
        </div>
        <span class="cell-muted">{formatBytes(total())} on disk</span>
        <span class="resource-count">{filteredImages().length}</span>
      </div>
      <Show
        when={filteredImages().length > 0}
        fallback={
          <div class="resource-empty">
            {fleet.images.length === 0
              ? `No images on ${hostID()}. Images pulled on this host will show up here.`
              : "No images match this search."}
          </div>
        }
      >
        <div class="data-scroll">
          <div class="data-table cols-image-list">
            <div class="data-head">
              <span>Repository</span>
              <span>Tag</span>
              <span>Size</span> <span>Used by</span>
              <span>Action</span>
            </div>
            <For each={filteredImages()}>{(image) => <ImageRow hostID={hostID()} image={image} />}</For>
          </div>
        </div>
      </Show>
    </section>
  );
}

function ImageRow(props: { hostID: string; image: Image }) {
  const ref = () => splitImageRef(props.image.tags?.[0] ?? "");
  const extraTags = () => Math.max(0, (props.image.tags?.length ?? 0) - 1);
  return (
    <div class="data-row">
      <div class="cell-name">
        <span class="resource-icon image" aria-hidden="true">
          ◈
        </span>
        <div>
          <A class="volume-name" href={imagePath(props.hostID, props.image.id)}>
            {ref().name || "untagged image"}
          </A>
          <span class="cell-muted" title={props.image.tags?.join(", ")}>
            {ref().path || props.image.id.slice(0, 12)}
          </span>
        </div>
      </div>
      <div class="tag-cell">
        <span class="chip-code">{ref().tag || "none"}</span>
        <Show when={extraTags() > 0}>
          <span class="cell-muted">+{extraTags()} more</span>
        </Show>
      </div>
      <span class="cell-value">{formatBytes(props.image.size)}</span>
      <div class="container-chips">
        <Show
          when={(props.image.containers?.length ?? 0) > 0}
          fallback={
            <span class="cell-muted">{props.image.used_outside_prefix ? "Used outside this prefix" : "Not used"}</span>
          }
        >
          <For each={props.image.containers}>
            {(use) => (
              <ContainerChip
                href={hostPath(props.hostID, "containers")}
                container={use.container}
                state={use.state}
                title={`${use.container} runs this image`}
              />
            )}
          </For>
        </Show>
      </div>
      <RemoveButton
        label="Remove"
        question={removalQuestion(props.image, props.hostID)}
        blocked={blockedBy(props.image)}
        blockedLabel={blockedLabel(props.image)}
        onRemove={() => removeImage(props.image.id)}
      />
    </div>
  );
}

/** One image: its configuration, the build steps it was made from and the containers that run it. */
export function ImageDetailPage() {
  const location = useLocation();
  const hostID = () => fleet.selectedHost;
  const navigate = useNavigate();
  const imageID = () => parseHostPath(location.pathname)?.imageID ?? "";
  const [image, setImage] = createSignal<ImageDetail | null>(null);
  const [loadError, setLoadError] = createSignal("");
  const [copied, setCopied] = createSignal(false);
  // Answers for an earlier image are dropped, as in the other detail pages.
  let request = 0;

  async function load() {
    const host = hostID();
    const id = ++request;
    const requested = imageID();
    setLoadError("");
    try {
      const current = await getImage(host, requested);
      if (id !== request) return;
      setImage(current);
    } catch (cause) {
      if (id === request && !sessionEnded(cause))
        setLoadError(cause instanceof Error ? cause.message : "Could not load the image.");
    }
  }

  createEffect(() => {
    void hostID();
    void imageID();
    setImage(null);
    void load();
  });

  function copyID(value: string) {
    navigator.clipboard?.writeText(value).catch(() => {});
    setCopied(true);
    setTimeout(() => setCopied(false), 1600);
  }

  // The image's name without registry or tag, as the list shows it; its short ID until it loads.
  const title = () => {
    const current = image();
    if (!current) return imageID().slice(0, 12);
    return splitImageRef(current.tags?.[0] ?? "").name || "untagged image";
  };

  const layers = () => {
    const steps = image()?.layers ?? [];
    const largest = Math.max(0, ...steps.map((step) => step.size));
    return steps.map((step, index) => ({
      number: String(index + 1).padStart(2, "0"),
      command: step.created_by,
      size: formatBytes(step.size),
      percent: step.size === 0 || largest === 0 ? 0 : Math.max(2, Math.round((step.size / largest) * 100)),
    }));
  };

  return (
    <>
      <PageHeader
        crumbs={[...hostCrumbs(hostID()), { label: "Images", href: hostPath(hostID(), "images") }, { label: title() }]}
        title={title()}
        badge={
          <For each={(image()?.tags ?? []).map((tag) => splitImageRef(tag).tag).filter(Boolean)}>
            {(tag) => <span class="tag-chip">{tag}</span>}
          </For>
        }
        actions={
          <Show when={image()}>
            {(current) => (
              <RemoveButton
                variant="header"
                label="Remove"
                question={removalQuestion(current(), hostID())}
                blocked={blockedBy(current())}
                blockedLabel={blockedLabel(current())}
                onRemove={async () => {
                  await removeImage(current().id);
                  navigate(hostPath(hostID(), "images"));
                }}
              />
            )}
          </Show>
        }
      >
        <Show when={image()}>
          {(current) => <span class="header-reference">{current().tags?.[0] ?? current().id}</span>}
        </Show>
      </PageHeader>
      <section class="panel resource-page stack-detail image-detail">
        <Show when={loadError()}>
          <div class="resource-empty">{loadError()}</div>
        </Show>
        <Show when={image()}>
          {(current) => (
            <div class="stack-detail-grid">
              <div class="stack-detail-block">
                <h3>Details</h3>
                <dl class="fact-list">
                  <dt>Size</dt>
                  <dd>{formatBytes(current().size)}</dd>
                  <dt>Created</dt>
                  <dd>{new Date(current().created * 1000).toLocaleString()}</dd>
                  <dt>Platform</dt>
                  <dd>{current().platform}</dd>
                </dl>
              </div>
              <div class="stack-detail-block detail-wide">
                <h3>Image ID</h3>
                <div class="copy-box">
                  <code>{current().id}</code>
                  <button
                    class="copy-button"
                    type="button"
                    aria-label="Copy image ID"
                    onClick={() => copyID(current().id)}
                  >
                    {copied() ? "Copied" : "Copy ID"}
                  </button>
                </div>
              </div>
              <div class="stack-detail-block">
                <h3>Configuration</h3>
                <dl class="fact-list">
                  <dt>Command</dt>
                  <dd>{current().command?.join(" ") || "None"}</dd>
                  <dt>Entrypoint</dt>
                  <dd>{current().entrypoint?.join(" ") || "None"}</dd>
                  <dt>Working dir</dt>
                  <dd>{current().working_dir || "/"}</dd>
                  <dt>Exposed ports</dt>
                  <dd>{current().ports?.join(", ") || "None"}</dd>
                </dl>
              </div>
              <div class="stack-detail-block">
                <h3>Environment</h3>
                <Show when={(current().env?.length ?? 0) > 0} fallback={<p class="stack-note">No environment.</p>}>
                  <dl class="fact-list">
                    <For each={current().env}>
                      {(entry) => {
                        const cut = entry.indexOf("=");
                        return (
                          <>
                            <dt>{cut === -1 ? entry : entry.slice(0, cut)}</dt>
                            <dd>{cut === -1 ? "" : entry.slice(cut + 1)}</dd>
                          </>
                        );
                      }}
                    </For>
                  </dl>
                </Show>
              </div>
              <div class="stack-detail-block detail-wide">
                <h3>Used by</h3>
                <Show
                  when={(current().containers?.length ?? 0) > 0}
                  fallback={
                    <p class="stack-note">
                      {current().used_outside_prefix
                        ? "No container of this prefix runs the image. A container outside this prefix does."
                        : "No container of this prefix runs the image."}
                    </p>
                  }
                >
                  <div class="container-chips">
                    <For each={current().containers}>
                      {(use) => (
                        <ContainerChip
                          href={hostPath(hostID(), "containers")}
                          container={use.container}
                          state={use.state}
                        />
                      )}
                    </For>
                  </div>
                </Show>
                <p class="stack-note">
                  Images belong to the Podman installation on this host, not to one prefix, so other prefixes may use it
                  too.
                </p>
              </div>
              <div class="stack-detail-block detail-wide">
                <h3>Layers</h3>
                <Show when={layers().length > 0} fallback={<p class="stack-note">No build steps recorded.</p>}>
                  <div class="data-scroll">
                    <div class="data-table cols-image-layers">
                      <div class="data-head">
                        <span>#</span>
                        <span>Step</span>
                        <span>Share of largest</span>
                        <span>Size</span>
                      </div>
                      <For each={layers()}>
                        {(layer) => (
                          <div class="data-row">
                            <span class="cell-muted">{layer.number}</span>
                            <span class="layer-command" title={layer.command}>
                              {layer.command}
                            </span>
                            <span class="layer-track" aria-hidden="true">
                              <span class="layer-fill" style={{ width: `${layer.percent}%` }} />
                            </span>
                            <span class="cell-value">{layer.size}</span>
                          </div>
                        )}
                      </For>
                    </div>
                  </div>
                </Show>
              </div>
            </div>
          )}
        </Show>
      </section>
    </>
  );
}
