import { DomainPage, useRouteTab } from "../../components/layout";
import { ArtworkSettings } from "./Artwork";
import { CatalogueSettings } from "./Catalogue";
import { EpisodeOrders } from "./EpisodeOrder";
import { RatingSources } from "./Ratings";

const TABS = [
  { key: "ratings", label: "Ratings" },
  { key: "artwork", label: "Artwork" },
  { key: "episodes", label: "Episode order" },
  { key: "catalogue", label: "Catalogue" },
] as const;

/** How titles look in Plex: rating badges, posters, episode order and the TMDB catalogue. */
export default function Metadata() {
  const tab = useRouteTab(TABS);
  return (
    <DomainPage
      title="Metadata"
      description="How titles look in your viewers’ Plex apps: ratings, artwork, episode order and the catalogue behind them."
      base="/metadata"
      tabs={TABS}
      active={tab}
    >
      {tab === "ratings" ? <RatingSources /> : null}
      {tab === "artwork" ? <ArtworkSettings /> : null}
      {tab === "episodes" ? <EpisodeOrders /> : null}
      {tab === "catalogue" ? <CatalogueSettings /> : null}
    </DomainPage>
  );
}
