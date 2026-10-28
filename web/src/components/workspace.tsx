import { cn } from "@braedonsaunders/appkit-ui";
import type { ReactNode } from "react";

/**
 * Full-height studio pages (HomeScreen, Live TV): a one-line header, then
 * controls on the left third and the live result on the right two thirds,
 * each scrolling on its own. Below `lg` the two stack and scroll together.
 */
export function Workspace({
  title,
  subtitle,
  actions,
  aside,
  asideLabel,
  asideClassName,
  main,
  mainLabel,
  mainClassName,
  children,
}: {
  title: string;
  subtitle?: ReactNode;
  actions?: ReactNode;
  aside?: ReactNode;
  asideLabel?: string;
  asideClassName?: string;
  main?: ReactNode;
  mainLabel?: string;
  mainClassName?: string;
  /** Rendered instead of the split (loading, empty and error states). */
  children?: ReactNode;
}) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex min-h-14 shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-border bg-surface px-3 py-2.5 sm:px-5">
        <div className="flex min-w-0 flex-1 items-baseline gap-3">
          <h1 className="shrink-0 text-lg font-semibold tracking-tight text-fg">{title}</h1>
          {subtitle ? (
            <p className="hidden min-w-0 truncate text-sm text-fg-muted md:block">{subtitle}</p>
          ) : null}
        </div>
        {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
      </header>
      {children ?? (
        <div className="app-scroll min-h-0 flex-1 overflow-y-auto lg:grid lg:grid-cols-3 lg:overflow-hidden">
          <aside
            className={cn(
              "app-scroll flex flex-col border-b border-border bg-bg-subtle lg:col-span-1 lg:min-h-0 lg:overflow-y-auto lg:border-b-0 lg:border-r",
              asideClassName,
            )}
            aria-label={asideLabel}
          >
            {aside}
          </aside>
          <main
            className={cn(
              "app-scroll min-w-0 bg-bg lg:col-span-2 lg:min-h-0 lg:overflow-y-auto",
              mainClassName,
            )}
            aria-label={mainLabel}
          >
            {main}
          </main>
        </div>
      )}
    </div>
  );
}

/** The pinned top of a workspace panel: toolbar row + sub-tabs, flush to the top. */
export function PanelHead({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cn(
        "sticky top-0 z-20 shrink-0 space-y-2 border-b border-border bg-bg-subtle/95 px-3 pt-3 backdrop-blur sm:px-4",
        className,
      )}
    >
      {children}
    </div>
  );
}
