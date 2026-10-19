import { Drawer as AppKitDrawer, type DrawerProps, cn } from "@braedonsaunders/appkit-ui";
import { createContext, useContext, useEffect, useId, useRef, type ReactNode } from "react";

const DrawerContext = createContext(false);

/** Associate AppKit's visible record title with the dialog's accessible name. */
function RecordTitle({ children }: { children: ReactNode }) {
  const id = useId();
  const ref = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    const dialog = ref.current?.closest('[role="dialog"]');
    if (dialog) dialog.setAttribute("aria-labelledby", id);
  }, [id]);
  return (
    <span ref={ref} id={id}>
      {children}
    </span>
  );
}

/** Preserve AppKit focus, scroll and Escape handling when editors open a child drawer. */
export function Drawer({ title, stacked, bodyClassName, ...props }: DrawerProps) {
  const nested = useContext(DrawerContext);
  return (
    <DrawerContext.Provider value={true}>
      <AppKitDrawer
        {...props}
        stacked={stacked ?? nested}
        bodyClassName={cn(
          "[&_fieldset]:min-w-0",
          bodyClassName ?? "overflow-y-auto px-4 py-5 sm:px-6",
        )}
        title={<RecordTitle>{title ?? "Editor"}</RecordTitle>}
      />
    </DrawerContext.Provider>
  );
}
