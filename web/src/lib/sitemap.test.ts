import { describe, expect, it } from "vitest";
import { destinations, PAGES } from "./sitemap";

describe("sitemap", () => {
  it("gives every destination exactly one home", () => {
    const hrefs = destinations().map((d) => d.href);
    expect(new Set(hrefs).size).toBe(hrefs.length);
    expect(PAGES.map((p) => p.title)).toContain("Overview");
  });
});
