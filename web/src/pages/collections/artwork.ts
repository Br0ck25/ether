/** Recover the original TMDB path from an admin image URL before saving it to Plex artwork. */
export function artworkSource(image: string | undefined): string | undefined {
  if (!image) return undefined;
  try {
    const url = new URL(image, "http://ether.local");
    if (url.pathname === "/ether/api/image") {
      const path = url.searchParams.get("path");
      return path && /^\/[A-Za-z0-9_-]{1,128}\.(jpg|jpeg|png|webp|svg)$/.test(path)
        ? path
        : undefined;
    }
    if (/^https?:\/\//.test(image)) return image;
    if (/^\/[A-Za-z0-9_-]{1,128}\.(jpg|jpeg|png|webp|svg)$/.test(image)) return image;
  } catch {
    /* An unrecognised image cannot be used as a server-side artwork source. */
  }
  return undefined;
}

/** Stored artwork paths need the admin image proxy in browser previews. */
export function artworkPreviewURL(source: string | undefined): string | undefined {
  if (!source) return undefined;
  return source.startsWith("/")
    ? `/ether/api/image?path=${encodeURIComponent(source)}&w=342`
    : source;
}
