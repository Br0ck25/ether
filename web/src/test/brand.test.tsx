import { act, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { BrandSplash } from "../components/brand";

afterEach(() => { vi.useRealTimers(); });

it("waits for bootstrap, then retires the splash for the lifetime of the app", () => {
  vi.useFakeTimers();
  const page = render(<BrandSplash ready={false} />);
  act(() => { vi.advanceTimersByTime(2500); });
  expect(page.container.querySelector("[data-brand-splash]")).toBeInTheDocument();
  page.rerender(<BrandSplash ready={true} />);
  act(() => { vi.advanceTimersByTime(401); });
  expect(page.container.querySelector("[data-brand-splash]")).not.toBeInTheDocument();
  page.rerender(<BrandSplash ready={false} />);
  expect(page.container.querySelector("[data-brand-splash]")).not.toBeInTheDocument();
});

it("shortens the reveal for reduced motion", () => {
  vi.useFakeTimers();
  const original = window.matchMedia;
  vi.spyOn(window, "matchMedia").mockImplementation(query => ({ ...original(query), matches: query.includes("prefers-reduced-motion") }));
  const page = render(<BrandSplash ready={true} />);
  act(() => { vi.advanceTimersByTime(351); });
  expect(page.container.querySelector("[data-brand-splash]")).not.toBeInTheDocument();
});
