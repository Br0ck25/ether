export type Overlays = {
  enabled: boolean;
  status: boolean;
  quality: boolean;
  ratings: boolean;
  personalize: boolean;
  localize: boolean;
  style: "glass" | "solid" | "minimal";
  position: "bottom" | "top";
  accent: string;
  size: "small" | "medium" | "large";
};
export const normalizeOverlays = (s: Partial<Overlays>): Overlays => ({
  enabled: false,
  status: false,
  quality: false,
  ratings: false,
  personalize: false,
  localize: false,
  ...s,
  style: s.style || "glass",
  position: s.position || "bottom",
  accent: s.accent || "#d4a574",
  size: s.size || "medium",
});
export const PRESETS: { name: string; description: string; settings: Partial<Overlays> }[] = [
  {
    name: "Cinema",
    description: "Glass · bottom · small",
    settings: {
      enabled: true,
      style: "glass",
      position: "bottom",
      accent: "#d4a574",
      size: "small",
      quality: true,
      ratings: true,
      status: false,
    },
  },
  {
    name: "Spotlight",
    description: "Solid · top · medium",
    settings: {
      enabled: true,
      style: "solid",
      position: "top",
      accent: "#a7c4b5",
      size: "medium",
      quality: true,
      ratings: true,
      status: true,
    },
  },
  {
    name: "Essential",
    description: "Minimal · ratings only",
    settings: {
      enabled: true,
      style: "minimal",
      position: "bottom",
      accent: "#e8e4dc",
      size: "small",
      quality: false,
      ratings: true,
      status: false,
    },
  },
];
export function previewURL(
  settings: Overlays,
  key: string,
  user?: number | null,
  lang = "en",
  nonce = 0,
) {
  const params = new URLSearchParams({
    settings: JSON.stringify(settings),
    lang,
    v: String(nonce),
  });
  if (user != null) params.set("userId", String(user));
  return `/ether/api/discovery/overlays/preview/${encodeURIComponent(key)}?${params}`;
}
