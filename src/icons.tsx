// Icons, from Phosphor (solidjs-phosphor). Every page imports them from
// here, so the choice of glyph for each meaning lives in one place. All are
// decorative: the text next to each says what it means. They draw in
// currentColor; the class "icon" lets CSS place and colour them.

import {
  PhArrowClockwiseIcon,
  PhArrowLeftIcon,
  PhArrowRightIcon,
  PhArrowUpIcon,
  PhCaretRightIcon,
  PhCheckIcon,
  PhCircleNotchIcon,
  PhClockIcon,
  PhCubeIcon,
  PhDatabaseIcon,
  PhEyeIcon,
  PhEyeSlashIcon,
  PhHardDrivesIcon,
  PhInfoIcon,
  PhKeyIcon,
  PhLockSimpleIcon,
  PhLockSimpleOpenIcon,
  PhMagnifyingGlassIcon,
  PhPackageIcon,
  PhPencilSimpleIcon,
  PhPlayIcon,
  PhPlusIcon,
  PhProhibitIcon,
  PhSlidersHorizontalIcon,
  PhSquaresFourIcon,
  PhStackSimpleIcon,
  PhStopIcon,
  PhTrashIcon,
  PhWarningIcon,
  PhXIcon,
} from "solidjs-phosphor";

type Props = { size?: number };

export const LockIcon = (props: Props) => <PhLockSimpleIcon class="icon" size={props.size ?? 15} aria-hidden="true" />;
export const UnlockedIcon = (props: Props) => (
  <PhLockSimpleOpenIcon class="icon" size={props.size ?? 15} aria-hidden="true" />
);
/** No secret store at all. */
export const NoLockIcon = (props: Props) => <PhProhibitIcon class="icon" size={props.size ?? 18} aria-hidden="true" />;
export const KeyIcon = (props: Props) => <PhKeyIcon class="icon" size={props.size ?? 15} aria-hidden="true" />;
export const ClockIcon = (props: Props) => <PhClockIcon class="icon" size={props.size ?? 13} aria-hidden="true" />;
export const WarningIcon = (props: Props) => <PhWarningIcon class="icon" size={props.size ?? 16} aria-hidden="true" />;
export const GridIcon = (props: Props) => <PhSquaresFourIcon class="icon" size={props.size ?? 16} aria-hidden="true" />;
export const BackIcon = (props: Props) => <PhArrowLeftIcon class="icon" size={props.size ?? 15} aria-hidden="true" />;
/** A host. */
export const ServerIcon = (props: Props) => (
  <PhHardDrivesIcon class="icon" size={props.size ?? 19} aria-hidden="true" />
);
export const ContainerIcon = (props: Props) => <PhCubeIcon class="icon" size={props.size ?? 16} aria-hidden="true" />;
export const ImageIcon = (props: Props) => (
  <PhStackSimpleIcon class="icon" size={props.size ?? 16} aria-hidden="true" />
);
export const VolumeIcon = (props: Props) => <PhDatabaseIcon class="icon" size={props.size ?? 16} aria-hidden="true" />;
/** A Compose stack. */
export const StackIcon = (props: Props) => <PhPackageIcon class="icon" size={props.size ?? 16} aria-hidden="true" />;
/** The plus of a create button; the button's CSS colours it. */
export const PlusIcon = (props: Props) => (
  <PhPlusIcon class="icon" size={props.size ?? 14} weight="bold" aria-hidden="true" />
);
export const SearchIcon = (props: Props) => (
  <PhMagnifyingGlassIcon class="icon" size={props.size ?? 16} aria-hidden="true" />
);
export const ChevronRightIcon = (props: Props) => (
  <PhCaretRightIcon class="icon" size={props.size ?? 18} aria-hidden="true" />
);
export const ArrowRightIcon = (props: Props) => (
  <PhArrowRightIcon class="icon" size={props.size ?? 18} aria-hidden="true" />
);
/** Filled; the button's CSS colours it. */
export const PlayIcon = (props: Props) => (
  <PhPlayIcon class="icon icon-fill" size={props.size ?? 12} weight="fill" aria-hidden="true" />
);
export const StopIcon = (props: Props) => (
  <PhStopIcon class="icon icon-fill" size={props.size ?? 12} weight="fill" aria-hidden="true" />
);
export const EyeIcon = (props: Props) => <PhEyeIcon class="icon" size={props.size ?? 18} aria-hidden="true" />;
export const EyeSlashIcon = (props: Props) => (
  <PhEyeSlashIcon class="icon" size={props.size ?? 18} aria-hidden="true" />
);

/** The brand's four-point star. A logo, not an icon, so it stays the design's own shape. */
export function BrandMark() {
  return (
    <svg class="brand-mark" width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
      <path d="M12 2C12.6 7.4 16.6 11.4 22 12C16.6 12.6 12.6 16.6 12 22C11.4 16.6 7.4 12.6 2 12C7.4 11.4 11.4 7.4 12 2Z" />
    </svg>
  );
}
export const InfoIcon = (props: Props) => <PhInfoIcon class="icon" size={props.size ?? 15} aria-hidden="true" />;
export const RefreshIcon = (props: Props) => (
  <PhArrowClockwiseIcon class="icon" size={props.size ?? 16} aria-hidden="true" />
);
export const ArrowUpIcon = (props: Props) => <PhArrowUpIcon class="icon" size={props.size ?? 13} aria-hidden="true" />;
export const PencilIcon = (props: Props) => (
  <PhPencilSimpleIcon class="icon" size={props.size ?? 15} aria-hidden="true" />
);
export const TrashIcon = (props: Props) => <PhTrashIcon class="icon" size={props.size ?? 15} aria-hidden="true" />;
/** The fleet-wide Settings page. */
export const SettingsIcon = (props: Props) => (
  <PhSlidersHorizontalIcon class="icon" size={props.size ?? 16} aria-hidden="true" />
);
export const CheckIcon = (props: Props) => (
  <PhCheckIcon class="icon" size={props.size ?? 18} weight="bold" aria-hidden="true" />
);
/** Close or dismiss. */
export const CloseIcon = (props: Props) => (
  <PhXIcon class="icon" size={props.size ?? 14} weight="bold" aria-hidden="true" />
);
/** Turns while something is in progress; the "spinner" class stops under reduced motion. */
export const SpinnerIcon = (props: Props) => (
  <PhCircleNotchIcon class="icon spinner" size={props.size ?? 14} weight="bold" aria-hidden="true" />
);
