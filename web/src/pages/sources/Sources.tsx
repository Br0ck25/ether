import { Button } from "@braedonsaunders/appkit-ui";
import { Download } from "lucide-react";
import { useState } from "react";
import { Drawer } from "../../components/Drawer";
import { DomainPage, useRouteTab } from "../../components/layout";
import { ProwlarrImport } from "../../components/ProwlarrImport";
import { HealthStrip } from "./form";
import { IndexerList } from "./Indexers";
import { UsenetProviders } from "./Usenet";
import { SourceUsage } from "./Usage";

const TABS = [
  { key: "usenet", label: "Usenet providers" },
  { key: "indexers", label: "Indexers" },
  { key: "usage", label: "Usage & costs" },
] as const;

/** Where releases come from: Usenet providers, indexers, and what they cost. */
export default function Sources() {
  const tab = useRouteTab(TABS);
  const [importing, setImporting] = useState(false);
  const [version, setVersion] = useState(0);
  return (
    <DomainPage
      title="Sources"
      description="The Usenet providers and indexers Ether streams from."
      base="/sources"
      tabs={TABS}
      active={tab}
      status={<HealthStrip ids={["usenet", "indexers"]} />}
      actions={
        tab === "indexers" ? (
          <Button variant="outline" size="sm" onClick={() => setImporting(true)}>
            <Download size={14} /> Import from Prowlarr
          </Button>
        ) : null
      }
    >
      {tab === "usenet" ? <UsenetProviders /> : null}
      {tab === "indexers" ? <IndexerList version={version} /> : null}
      {tab === "usage" ? <SourceUsage /> : null}
      <Drawer
        open={importing}
        onClose={() => setImporting(false)}
        title="Import from Prowlarr"
        description="Reuse the Usenet indexers you already manage."
        size="lg"
      >
        <ProwlarrImport onImported={() => setVersion((v) => v + 1)} />
      </Drawer>
    </DomainPage>
  );
}
