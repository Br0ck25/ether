import type { ThemeOptions } from "@braedonsaunders/appkit-ui/theme-script";

/**
 * Dark by default; the owner can switch to light from the header. Shared by
 * <ThemeProvider> and the pre-paint script vite.config.ts injects into
 * index.html, so the first paint always matches the hydrated theme.
 */
export const theme = { storageKey: "ether-theme", defaultTheme: "dark", enableSystem: false } as const satisfies ThemeOptions;
