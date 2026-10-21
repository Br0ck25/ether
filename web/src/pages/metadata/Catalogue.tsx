import { Button, SettingsSection } from "@braedonsaunders/appkit-ui";
import { useState } from "react";
import { Database } from "lucide-react";
import { useApp } from "../../app-context";
import { ConfigSection } from "../../components/ConfigSection";
import { TestButton, type TestState } from "../../components/fields";
import { SettingsRow } from "../../components/SettingsRow";
import { LevelBadge } from "../../components/status";
import { Section } from "../../components/layout";
import { TitleSearch } from "../../components/pickers";
import { Artwork } from "../../components/Artwork";
import { api } from "../../lib/api";
import type { TitleCard } from "../../lib/contract";
import { MetadataWorkspace } from "./MetadataWorkspace";
import * as S from "../settings/schema";

function CatalogueStatus() {
  const { health } = useApp();
  const item = health.find((h) => h.id === "catalog");
  if (!item) return null;
  return (
    <SettingsSection title="Catalogue status">
      <SettingsRow
        title="TMDB catalogue"
        description={item.details.join(" · ") || undefined}
        control={<LevelBadge level={item.level} label={item.summary} />}
      />
    </SettingsSection>
  );
}

export function CatalogueSettings() {
  const [test, setTest] = useState<TestState>({ status: "idle" });
  const [title, setTitle] = useState<TitleCard | null>(null);
  const runTest = (key: string) => {
    setTest({ status: "running" });
    api.testTMDB(key).then(
      (r) =>
        setTest(
          r.ok
            ? { status: "ok", durationMs: r.durationMs }
            : { status: "fail", message: r.error ?? "failed" },
        ),
      (e) => setTest({ status: "fail", message: (e as Error).message }),
    );
  };
  return (
    <MetadataWorkspace
      aside={
        <ConfigSection
          configKey="catalog"
          endpoint="catalog"
          groups={S.CATALOG}
          label="Catalogue"
          test={(draft) => (
            <TestButton
              label="Test TMDB key"
              state={test}
              onTest={() => runTest(String(draft.tmdbKey ?? ""))}
            />
          )}
        />
      }
      main={
        <div className="space-y-4">
          <CatalogueStatus />
          <Section
            title="Inspect catalogue metadata"
            description="Search the current catalogue to check a title’s artwork and metadata."
            actions={
              title ? (
                <Button variant="ghost" size="sm" onClick={() => setTitle(null)}>
                  Clear title
                </Button>
              ) : undefined
            }
          >
            <TitleSearch placeholder="Search movies and shows…" onPick={setTitle} />
            {title ? (
              <div className="flex items-start gap-5">
                <Artwork
                  src={title.poster}
                  alt={`${title.title} poster`}
                  className="aspect-[2/3] w-36 shrink-0 rounded-md"
                />
                <div className="min-w-0 flex-1">
                  <h3 className="text-sm font-semibold">{title.title}</h3>
                  <dl className="metadata-detail-list">
                    {[
                      ["Year", title.year || "Unknown"],
                      ["Type", title.type],
                      ["Catalogue ID", title.key],
                    ].map(([label, value]) => (
                      <div key={label}>
                        <dt>{label}</dt>
                        <dd>{value}</dd>
                      </div>
                    ))}
                  </dl>
                </div>
              </div>
            ) : (
              <div className="metadata-empty">
                <Database size={24} />
                <h2>No title selected</h2>
                <p>Search above to inspect a movie or show.</p>
              </div>
            )}
          </Section>
          <p className="text-xs text-fg-muted">
            Catalogue settings apply to new requests. Unknown and future releases are excluded from
            browsing.
          </p>
        </div>
      }
    />
  );
}
