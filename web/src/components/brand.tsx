import { cn } from "@braedonsaunders/appkit-ui";

/** Ether's mark: a play glyph inside an orbit, drawn in the brand token. */
export function EtherMark({ size = 28, className }: { size?: number; className?: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden className={cn("shrink-0", className)}>
      <rect x="1" y="1" width="30" height="30" rx="9" className="fill-primary" />
      <ellipse cx="16" cy="16" rx="11" ry="5.2" transform="rotate(-28 16 16)" fill="none" strokeWidth="1.6" className="stroke-primary-fg opacity-50" />
      <path d="M13 10.8v10.4c0 .7.8 1.1 1.4.7l7.6-5.2c.5-.4.5-1.1 0-1.4l-7.6-5.2c-.6-.4-1.4 0-1.4.7Z" className="fill-primary-fg" />
    </svg>
  );
}

export function Brand({ serverName }: { serverName?: string }) {
  return (
    <span className="flex min-w-0 items-center gap-2.5">
      <EtherMark size={28} />
      <span className="min-w-0 leading-tight">
        <span className="block text-sm font-semibold tracking-tight text-fg">Ether</span>
        {serverName ? <span className="block truncate text-[11px] text-fg-muted">{serverName}</span> : null}
      </span>
    </span>
  );
}
