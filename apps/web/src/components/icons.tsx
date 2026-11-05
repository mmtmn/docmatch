// Inline SVG icons (no icon font, no third-party script). All are decorative: the text next to
// them carries the meaning, so each one is aria-hidden by default.
import type { SVGProps } from 'react';
import type { FileKind } from '../lib/precheck';

type IconProps = SVGProps<SVGSVGElement> & { size?: number };

function Svg({ size = 20, children, ...rest }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  );
}

export function InfoIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <circle cx="12" cy="12" r="9.5" />
      <path d="M12 11v6" />
      <circle cx="12" cy="7.6" r="0.9" fill="currentColor" stroke="none" />
    </Svg>
  );
}

export function CloseIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M6 6l12 12M18 6L6 18" />
    </Svg>
  );
}

export function UploadIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M12 16V4M7 9l5-5 5 5" />
      <path d="M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3" />
    </Svg>
  );
}

export function CheckIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M5 12.5l4.5 4.5L19 7.5" />
    </Svg>
  );
}

export function WarningIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M12 3.5l9.5 16.5h-19z" />
      <path d="M12 10v4.5" />
      <circle cx="12" cy="17.2" r="0.9" fill="currentColor" stroke="none" />
    </Svg>
  );
}

export function AlertIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <circle cx="12" cy="12" r="9.5" />
      <path d="M12 7v6" />
      <circle cx="12" cy="16.4" r="0.9" fill="currentColor" stroke="none" />
    </Svg>
  );
}

export function TildeIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M4 13c2-4 5-4 8 0s6 4 8 0" />
    </Svg>
  );
}

export function ChevronIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M6 9l6 6 6-6" />
    </Svg>
  );
}

export function SearchIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <circle cx="11" cy="11" r="6.5" />
      <path d="M20 20l-4.2-4.2" />
    </Svg>
  );
}

export function PlayIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M8 5.5v13l10-6.5z" fill="currentColor" stroke="none" />
    </Svg>
  );
}

export function DownloadIcon(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M12 4v12M7 11l5 5 5-5" />
      <path d="M4 17v2a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-2" />
    </Svg>
  );
}

function DocumentFrame({ children, ...props }: IconProps) {
  return (
    <Svg {...props}>
      <path d="M7 3h7l5 5v12a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z" />
      <path d="M14 3v5h5" />
      {children}
    </Svg>
  );
}

export function FileKindIcon({ kind, ...props }: IconProps & { kind: FileKind }) {
  switch (kind) {
    case 'csv':
      return (
        <DocumentFrame {...props}>
          <path d="M9 12h6M9 15h6M9 18h6" />
        </DocumentFrame>
      );
    case 'xlsx':
      return (
        <DocumentFrame {...props}>
          <path d="M9 12h6v6H9zM12 12v6M9 15h6" />
        </DocumentFrame>
      );
    case 'doc':
      return (
        <DocumentFrame {...props}>
          <path d="M9 11h6M9 13.5h6M9 16h6M9 18.5h4" />
        </DocumentFrame>
      );
    case 'pdf':
      return (
        <DocumentFrame {...props}>
          <path d="M9 17v-5h1.6a1.5 1.5 0 0 1 0 3H9M14 17v-5h1.4" />
        </DocumentFrame>
      );
    case 'xml':
    case 'json':
    case 'html':
      return (
        <DocumentFrame {...props}>
          <path d="M10.5 12l-2 2.5 2 2.5M13.5 12l2 2.5-2 2.5" />
        </DocumentFrame>
      );
    case 'zip':
    case '7z':
      return (
        <DocumentFrame {...props}>
          <path d="M11 9h2M11 11h2M11 13h2M11 15h2M12 15v3" />
        </DocumentFrame>
      );
    case 'eml':
    case 'msg':
      return (
        <Svg {...props}>
          <rect x="3" y="5" width="18" height="14" rx="2" />
          <path d="M3.5 7l8.5 6 8.5-6" />
        </Svg>
      );
    case 'ofx':
      return (
        <Svg {...props}>
          <path d="M3 9l9-5 9 5" />
          <path d="M5 9v9M9.5 9v9M14.5 9v9M19 9v9M3 20h18" />
        </Svg>
      );
    case 'image':
      return (
        <Svg {...props}>
          <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
          <circle cx="9" cy="9.5" r="1.6" />
          <path d="M20 16l-5-5-8 8" />
        </Svg>
      );
    case 'audio':
      return (
        <Svg {...props}>
          <path d="M4 10v4M8 7v10M12 4v16M16 8v8M20 11v2" />
        </Svg>
      );
    case 'video':
      return (
        <Svg {...props}>
          <rect x="3" y="6" width="13" height="12" rx="2" />
          <path d="M16 10l5-3v10l-5-3z" />
        </Svg>
      );
    case 'txt':
    default:
      return (
        <DocumentFrame {...props}>
          <path d="M9 13h6M9 16h4" />
        </DocumentFrame>
      );
  }
}
