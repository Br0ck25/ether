import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

// Node 25+ defines its own (file-backed, here unconfigured) Web Storage
// globals, which stop vitest from installing jsdom's. Use jsdom's.
const dom = (globalThis as { jsdom?: { window: Window } }).jsdom;
if (dom) {
  for (const key of ["localStorage", "sessionStorage"] as const) {
    Object.defineProperty(globalThis, key, { configurable: true, writable: true, value: dom.window[key] });
  }
}

// jsdom lacks a few browser APIs that AppKit and the app use.
if (!window.matchMedia) {
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: (query: string) => ({
      matches: query.includes("min-width"), // render the desktop layout in tests
      media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }),
  });
}

class NoopObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return []; }
}
if (!("ResizeObserver" in window)) Object.defineProperty(window, "ResizeObserver", { writable: true, value: NoopObserver });
if (!("IntersectionObserver" in window)) Object.defineProperty(window, "IntersectionObserver", { writable: true, value: NoopObserver });
if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {};
if (!Element.prototype.scrollBy) Element.prototype.scrollBy = () => {};
if (!window.scrollTo) window.scrollTo = () => {};

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});
