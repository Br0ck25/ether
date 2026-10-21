import { useRouteTab } from "../../components/layout";
import { ArtworkSettings } from "./Artwork";
import { CatalogueSettings } from "./Catalogue";
import { EpisodeOrders } from "./EpisodeOrder";
import { RatingSources } from "./Ratings";
const TABS = [
  { key: "artwork" },
  { key: "ratings" },
  { key: "episodes" },
  { key: "catalogue" },
] as const;
export default function Metadata() {
  const tab = useRouteTab(TABS);
  if (tab === "ratings") return <RatingSources />;
  if (tab === "episodes") return <EpisodeOrders />;
  if (tab === "catalogue") return <CatalogueSettings />;
  return <ArtworkSettings />;
}
