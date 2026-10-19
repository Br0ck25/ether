/** Each new collection gets its own local-draft id, carried in the URL so reloads restore it. */
export function newCollectionHref(params: Record<string, string> = {}): string {
  const q = new URLSearchParams({ ...params, draft: `new-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 5)}` });
  return `/collections/new?${q}`;
}
