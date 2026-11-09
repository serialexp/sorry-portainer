// Stroke icons from the design, drawn in currentColor. All are decorative:
// the text next to each says what it means.

import type { JSX } from "solid-js";

function Icon(props: { size: number; children: JSX.Element; width?: number }) {
  return (
    <svg
      class="icon"
      width={props.size}
      height={props.size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width={props.width ?? 2}
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      {props.children}
    </svg>
  );
}

export function LockIcon(props: { size?: number }) {
  return (
    <Icon size={props.size ?? 15}>
      <rect x="5" y="11" width="14" height="9" rx="2" />
      <path d="M8 11V8a4 4 0 0 1 8 0v3" />
    </Icon>
  );
}

export function UnlockedIcon(props: { size?: number }) {
  return (
    <Icon size={props.size ?? 15}>
      <rect x="5" y="11" width="14" height="9" rx="2" />
      <path d="M8 11V8a4 4 0 0 1 7.5-2" />
    </Icon>
  );
}

/** A lock struck through: no secret store at all. */
export function NoLockIcon(props: { size?: number }) {
  return (
    <Icon size={props.size ?? 18}>
      <rect x="5" y="11" width="14" height="9" rx="2" />
      <path d="M8 11V8a4 4 0 0 1 8 0v3" />
      <path d="M3 3l18 18" />
    </Icon>
  );
}

export function KeyIcon(props: { size?: number }) {
  return (
    <Icon size={props.size ?? 15}>
      <circle cx="8" cy="15" r="4" />
      <path d="M11 12l9-9M17 6l3 3M15 8l2 2" />
    </Icon>
  );
}

export function ClockIcon(props: { size?: number }) {
  return (
    <Icon size={props.size ?? 13} width={2.2}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </Icon>
  );
}

export function WarningIcon(props: { size?: number }) {
  return (
    <Icon size={props.size ?? 16}>
      <path d="M12 3l9.5 17h-19z" />
      <path d="M12 10v4M12 17h.01" />
    </Icon>
  );
}
