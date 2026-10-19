import { Drawer } from "./Drawer";
import { Button } from "@braedonsaunders/appkit-ui";
import { useState, type ReactNode } from "react";

/** A readable page summary; the existing AppKit drawer owns the editing surface. */
export function EditPanel({
  title,
  description,
  action,
  children,
  summary,
  footer,
  onOpen,
  onDiscard,
  size = "lg",
  initialOpen = false,
  compact = false,
  open: controlledOpen,
  onOpenChange,
}: {
  title: string;
  description?: string;
  action?: string;
  children: ReactNode;
  summary?: ReactNode;
  footer?: ReactNode;
  onOpen?: () => void;
  onDiscard?: () => void;
  size?: "md" | "lg" | "xl";
  initialOpen?: boolean;
  compact?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const [localOpen, setLocalOpen] = useState(initialOpen);
  const open = controlledOpen ?? localOpen;
  const setOpen = (next: boolean) => {
    setLocalOpen(next);
    onOpenChange?.(next);
  };
  const close = () => {
    onDiscard?.();
    setOpen(false);
  };
  return (
    <>
      {compact ? (
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            onOpen?.();
            setOpen(true);
          }}
        >
          {action ?? "Edit"}
        </Button>
      ) : (
        <section className="rounded-xl border border-border bg-surface p-4 sm:p-5">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 space-y-1">
              <h2 className="text-base font-semibold">{title}</h2>
              {description && <p className="max-w-2xl text-sm text-fg-muted">{description}</p>}
              {summary && <div className="pt-2 text-sm text-fg-muted">{summary}</div>}
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                onOpen?.();
                setOpen(true);
              }}
            >
              {action ?? `Edit ${title.toLowerCase()}`}
            </Button>
          </div>
        </section>
      )}
      <Drawer
        open={open}
        onClose={close}
        title={title}
        description={description}
        size={size}
        footer={footer}
      >
        <div className="space-y-5">{children}</div>
      </Drawer>
    </>
  );
}
