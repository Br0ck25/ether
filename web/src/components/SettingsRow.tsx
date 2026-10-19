import { SettingsRow as AppKitSettingsRow, cn } from "@braedonsaunders/appkit-ui";
import type { ComponentProps } from "react";

/** Keep labels readable beside wide controls by stacking the row on phones. */
export function SettingsRow({ className, stacked, ...props }: ComponentProps<typeof AppKitSettingsRow>) {
  return <AppKitSettingsRow {...props} stacked={stacked} className={cn(!stacked && "max-sm:flex-col max-sm:items-stretch", className)} />;
}
