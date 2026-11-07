export type ImageRef = { path: string; name: string; tag: string };

/**
 * Splits "docker.io/library/caddy:latest" into its repository path, name and
 * tag. A colon before the last slash belongs to a registry port, not a tag.
 */
export function splitImageRef(ref: string): ImageRef {
  const slash = ref.lastIndexOf("/");
  const colon = ref.lastIndexOf(":");
  const hasTag = colon > slash;
  const repository = hasTag ? ref.slice(0, colon) : ref;
  const tag = hasTag ? ref.slice(colon + 1) : "";
  const cut = repository.lastIndexOf("/");
  return { path: cut === -1 ? "" : repository.slice(0, cut), name: repository.slice(cut + 1), tag };
}
