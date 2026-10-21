import { describe, expect, it } from "vitest";
import { artworkPreviewURL, artworkSource } from "./artwork";

describe("native collection poster sources", () => {
  it("unwraps the browser image proxy before saving a Plex poster", () => {
    expect(artworkSource("/ether/api/image?path=%2Fposter.jpg&w=342")).toBe("/poster.jpg");
    expect(artworkSource("https://ether.example/ether/api/image?path=%2Fposter.webp&w=342")).toBe(
      "/poster.webp",
    );
    expect(artworkPreviewURL("/poster.jpg")).toBe("/ether/api/image?path=%2Fposter.jpg&w=342");
  });
  it("preserves custom image URLs and refuses unusable or invalid proxy paths", () => {
    expect(artworkSource("https://images.example/poster.jpg")).toBe(
      "https://images.example/poster.jpg",
    );
    expect(artworkPreviewURL("https://images.example/poster.jpg")).toBe(
      "https://images.example/poster.jpg",
    );
    expect(artworkSource("data:image/svg+xml,...")).toBeUndefined();
    expect(artworkSource("/ether/api/image?path=..%2Fsecret")).toBeUndefined();
    expect(artworkSource(undefined)).toBeUndefined();
  });
});
