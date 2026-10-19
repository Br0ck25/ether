import { describe, expect, it } from "vitest";
import { destinations, legacyRedirect, PAGES } from "./sitemap";

describe("sitemap", () => {
  it("gives every destination exactly one home", () => {
    const hrefs = destinations().map((d) => d.href);
    expect(new Set(hrefs).size).toBe(hrefs.length);
    expect(PAGES.map((p) => p.title)).toContain("Overview");
  });

  it("sends pre-rework URLs to their new sections", () => {
    expect(legacyRedirect("/playback", "?tab=rules")).toBe("/releases/rules");
    expect(legacyRedirect("/playback", "?tab=compatibility")).toBe("/playback/devices");
    expect(legacyRedirect("/playback", "")).toBeNull();
    expect(legacyRedirect("/settings/usenet", "")).toBe("/sources/usenet");
    expect(legacyRedirect("/settings/catalog", "")).toBe("/metadata/catalogue");
    expect(legacyRedirect("/settings/server", "")).toBeNull();
    expect(legacyRedirect("/settings/x-personalize", "")).toBeNull();
    expect(legacyRedirect("/discovery", "")).toBe("/metadata/ratings");
    expect(legacyRedirect("/traces", "")).toBe("/releases/traces");
    expect(legacyRedirect("/designer", "")).toBe("/home-screens");
  });
});
