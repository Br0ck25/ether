import { ConfigSection } from "../../components/ConfigSection";
import { DomainPage, useRouteTab } from "../../components/layout";
import * as S from "../settings/schema";
import { ClientRulesTab } from "./ClientRules";
import { QualityTab } from "./Quality";
import { ScoringTab, StartupTab } from "./Scoring";
import { TracesTab } from "./Traces";

const TABS = [
  { key: "quality", label: "Quality profiles" },
  { key: "rules", label: "Client rules" },
  { key: "selection", label: "Selection & blocklist" },
  { key: "scoring", label: "Custom scoring" },
  { key: "startup", label: "Playback start" },
  { key: "traces", label: "Traces" },
] as const;

/** Which release Ether picks for each play, and why. */
export default function Releases() {
  const tab = useRouteTab(TABS);
  return (
    <DomainPage
      title="Releases"
      description="Choose the quality you want. Ether ranks releases for the app playing them and fails over when one breaks."
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
          omit={["optimisticStart", "healthRace", "customFormats", "customFormatScores"]}
        />
      ) : null}
      {tab === "scoring" ? <ScoringTab /> : null}
      {tab === "startup" ? <StartupTab /> : null}
      {tab === "traces" ? <TracesTab /> : null}
    </DomainPage>
  );
}
