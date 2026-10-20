import { ConfigSection } from "../../components/ConfigSection";
import { DomainPage, useRouteTab } from "../../components/layout";
import * as S from "../settings/schema";
import { BlocklistTab } from "./Blocklist";
import { ClientRulesTab } from "./ClientRules";
import { HistoryTab } from "./History";
import { PinnedTab } from "./Pinned";
import { QualityTab } from "./Quality";
import { ScoringTab, StartupTab } from "./Scoring";
import { SearchTab } from "./Search";

const TABS = [
  { key: "quality", label: "Quality profiles" },
  { key: "rules", label: "Client rules" },
  { key: "selection", label: "Selection rules" },
  { key: "scoring", label: "Custom scoring" },
  { key: "startup", label: "Playback start" },
  { key: "search", label: "Search" },
  { key: "pinned", label: "Pinned" },
  { key: "blocklist", label: "Blocklist" },
  { key: "history", label: "History" },
] as const;

/** Which release Ether picks for each play, and why. */
export default function Releases() {
  const tab = useRouteTab(TABS);
  return (
    <DomainPage
      title="Releases"
      description="Choose the quality you want. Ether ranks releases for the app playing them and fails over when one breaks; search a title to see why, pin a release or block one."
      base="/releases"
      tabs={TABS}
      active={tab}
    >
      {tab === "quality" ? <QualityTab /> : null}
      {tab === "rules" ? <ClientRulesTab /> : null}
      {tab === "selection" ? (
        <ConfigSection
          configKey="resolver"
          endpoint="resolver"
          groups={S.RESOLVER}
          label="Release selection"
          omit={[
            "optimisticStart",
            "healthRace",
            "sourcePreference",
            "sourceScoreMargin",
            "customFormats",
            "customFormatScores",
          ]}
        />
      ) : null}
      {tab === "scoring" ? <ScoringTab /> : null}
      {tab === "startup" ? <StartupTab /> : null}
      {tab === "search" ? <SearchTab /> : null}
      {tab === "pinned" ? <PinnedTab /> : null}
      {tab === "blocklist" ? <BlocklistTab /> : null}
      {tab === "history" ? <HistoryTab /> : null}
    </DomainPage>
  );
}
