import { SettingsSection } from "@braedonsaunders/appkit-ui";
import { useState } from "react";
import { useApp } from "../../app-context";
import { ConfigSection } from "../../components/ConfigSection";
import { TestButton, type TestState } from "../../components/fields";
import { SettingsRow } from "../../components/SettingsRow";
import { LevelBadge } from "../../components/status";
import { api } from "../../lib/api";
import * as S from "../settings/schema";

/** Catalogue sync status beside the settings that control it. */
function CatalogueStatus() {
  const { health } = useApp();
  const item = health.find((h) => h.id === "catalog");
  if (!item) return null;
  return (
    <SettingsSection title="Status">
      <SettingsRow
        title="Catalogue"
        description={item.details.join(" · ") || undefined}
        control={<LevelBadge level={item.level} label={item.summary} />}
      />
    </SettingsSection>
  );
}

/** Metadata › Catalogue: TMDB access, language and what's browsable, edited in place. */
export function CatalogueSettings() {
  const [test, setTest] = useState<TestState>({ status: "idle" });
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
    <ConfigSection
      configKey="catalog"
      endpoint="catalog"
      groups={S.CATALOG}
      label="Catalogue"
      before={() => <CatalogueStatus />}
      test={(draft) => (
        <TestButton
          label="Test TMDB key"
          state={test}
          onTest={() => runTest(String(draft.tmdbKey ?? ""))}
        />
      )}
    />
  );
}
