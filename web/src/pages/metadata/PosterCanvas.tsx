import { Film } from "lucide-react";
import { Artwork } from "../../components/Artwork";
import type { TitleCard } from "../../lib/contract";
import type { Overlays } from "./model";

export function PosterCanvas({
  src,
  original,
  title,
  mode,
  enabled,
  settings,
}: {
  src: string;
  original: string;
  title: TitleCard | null;
  mode: "poster" | "library" | "compare";
  enabled: boolean;
  settings: Overlays;
}) {
  const name = title?.title || "After the Tide";
  const poster = (url: string, label: string) => (
    <figure className="metadata-poster">
      <Artwork
        src={url}
        alt={`${name} ${label}`}
        title="Preview unavailable"
        eager
        className="aspect-[2/3] w-full rounded-md"
      />
      <figcaption>{label}</figcaption>
    </figure>
  );
  return (
    <div className={`metadata-stage metadata-stage-${mode}`}>
      {mode === "compare" ? (
        <div className="metadata-compare">
          {poster(original, "Original")}
          {poster(src, enabled ? "Your design" : "Overlays off")}
        </div>
      ) : mode === "library" ? (
        <div className="metadata-library">
          <div className="metadata-library-header">
            <span>
              <Film size={16} /> Library preview
            </span>
            <span>Sample layout</span>
          </div>
          <div className="metadata-library-row">
            <div className="metadata-library-neighbour neighbour-one">
              <span>
                THE
                <br />
                QUIET
                <br />
                HOURS
              </span>
            </div>
            {poster(src, name)}
            <div className="metadata-library-neighbour neighbour-two">
              <span>
                INTO
                <br />
                THE
                <br />
                BLUE
              </span>
            </div>
          </div>
          <p className="metadata-library-disclaimer">
            Illustrative layout. Plex layouts vary by app.
          </p>
        </div>
      ) : (
        <div className="metadata-poster-detail">
          {poster(src, title ? "Your title, your design" : "Original sample artwork")}
          <div className="metadata-inspector">
            <h3>{name}</h3>
            <p>
              {title
                ? [title.year, title.type === "show" ? "TV series" : "Movie"]
                    .filter(Boolean)
                    .join(" · ")
                : "Sample poster · fictional title"}
            </p>
            <dl className="metadata-detail-list">
              {[
                ["Artwork", enabled ? "Ether design" : "Original"],
                ["Finish", settings.style],
                ["Position", settings.position],
                ["Size", settings.size],
                ["Accent", settings.accent],
              ].map(([label, value]) => (
                <div key={label}>
                  <dt>{label}</dt>
                  <dd>{value}</dd>
                </div>
              ))}
            </dl>
            <h4>Layers</h4>
            <dl className="metadata-detail-list">
              {(
                [
                  ["Release status", settings.status],
                  ["Quality", settings.quality],
                  ["Ratings", settings.ratings],
                  ["Taste matching", settings.personalize],
                  ["Localization", settings.localize],
                ] as const
              ).map(([label, on]) => (
                <div key={label}>
                  <dt>{label}</dt>
                  <dd className={enabled && on ? "text-primary" : "text-fg-muted"}>
                    {enabled && on ? "On" : "Off"}
                  </dd>
                </div>
              ))}
            </dl>
            <p className="metadata-inspector-note">
              {title
                ? "Badges only appear for verified quality and available ratings."
                : "Sample badges demonstrate placement and style. Real titles use verified facts."}
            </p>
          </div>
        </div>
      )}
    </div>
  );
}
