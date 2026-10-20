import {
  Alert,
  AlertDescription,
  Badge,
  Button,
  EmptyState,
  Input,
  SearchSelect,
  SettingsSection,
  Textarea,
  confirmDialog,
  toast,
} from "@braedonsaunders/appkit-ui";
import { Download, Globe, RefreshCw, Upload } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Drawer } from "../../components/Drawer";
import { ErrorPanel, LoadingRows, MutationError } from "../../components/feedback";
import { ChipGroup, SecretInput } from "../../components/fields";
import { Section } from "../../components/layout";
import { SaveBar, useDraft } from "../../components/SaveBar";
import { SettingsRow } from "../../components/SettingsRow";
import { api, ApiError, newId } from "../../lib/api";
import type {
  CatalogEntry,
  EtherConfig,
  Indexer,
  IndexerSitesConfig,
  SiteDefinition,
  SiteSetting,
} from "../../lib/types";
import { BoolRow, TextRow, recordErrors, toastError } from "./form";

/** Built-in sites: indexers Ether searches itself from an installed definition. */
export function isBuiltin(ix: { kind?: string }) {
  return ix.kind === "cardigann";
}

export function isInfoSetting(s: SiteSetting) {
  return s.type.startsWith("info");
}

function settingDefault(s: SiteSetting): string {
  switch (s.type) {
    case "checkbox":
      return ["true", "1", "yes", "on"].includes((s.default ?? "").toLowerCase())
        ? "true"
        : "false";
    case "multi-select":
      return (s.defaults?.length ? s.defaults : s.default ? [s.default] : []).join(",");
    case "select":
      return s.default ?? s.options?.[0]?.value ?? "";
    default:
      return s.default ?? "";
  }
}

/** A new indexer for a definition, with the definition's defaults filled in. */
export function blankSiteIndexer(def: SiteDefinition, priority: number): Indexer {
  const settings: Record<string, string> = {};
  const secretSettings: Record<string, string> = {};
  for (const s of def.settings) {
    if (isInfoSetting(s)) continue;
    if (s.type === "password") secretSettings[s.name] = "";
    else settings[s.name] = settingDefault(s);
  }
  return {
    id: newId("site"),
    name: def.name,
    url: def.links[0] ?? "",
    apiKey: "",
    dailyLimit: 0,
    dailyGrabs: 0,
    priority,
    enabled: true,
    movieCategories: null,
    tvCategories: null,
    protocol: def.protocol === "torrent" ? "torrent" : "",
    kind: "cardigann",
    definition: def.id,
    settings,
    secretSettings,
  };
}

/** "Torrent" or "Usenet", as shown on built-in site badges. */
export function siteProtocolLabel(protocol?: string) {
  return protocol === "usenet" || protocol === "" ? "Usenet" : "Torrent";
}

function plainText(html: string) {
  return html
    .replace(/<br\s*\/?>/gi, " ")
    .replace(/<[^>]+>/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

function DefinitionBadges({ def }: { def: SiteDefinition }) {
  return (
    <>
      <Badge variant="outline">{siteProtocolLabel(def.protocol)}</Badge>
      {def.type ? <Badge variant="secondary">{def.type}</Badge> : null}
      {def.language ? <Badge variant="secondary">{def.language}</Badge> : null}
    </>
  );
}

/**
 * The definition's own settings (account, options, guidance) for one
 * indexer. Password settings go to `secretSettings`, which the server seals
 * and redacts like an API key.
 */
export function SiteSettingsFields({
  def,
  draft,
  onChange,
  errors,
}: {
  def: SiteDefinition;
  draft: Indexer;
  onChange: (patch: Partial<Indexer>) => void;
  errors: Record<string, string>;
}) {
  const settings = draft.settings ?? {};
  const secrets = draft.secretSettings ?? {};
  const setSetting = (name: string, v: string) =>
    onChange({ settings: { ...settings, [name]: v } });
  const setSecret = (name: string, v: string) =>
    onChange({ secretSettings: { ...secrets, [name]: v } });
  const fields = def.settings.filter((s) => !isInfoSetting(s) || s.label || s.default);
  if (!fields.length) return null;
  return (
    <SettingsSection title="Site settings" description={`Settings ${def.name} needs.`}>
      {fields.map((s) => {
        const label = s.label || s.name;
        const value = settings[s.name] ?? settingDefault(s);
        if (isInfoSetting(s))
          return (
            <SettingsRow
              key={s.name}
              stacked
              title={label}
              description={s.default ? plainText(s.default) : undefined}
            />
          );
        switch (s.type) {
          case "password":
            return (
              <SettingsRow key={s.name} stacked title={label}>
                <SecretInput
                  ariaLabel={label}
                  value={secrets[s.name] ?? ""}
                  onChange={(v) => setSecret(s.name, v)}
                  invalid={Boolean(errors[`secretSettings.${s.name}`])}
                />
              </SettingsRow>
            );
          case "checkbox":
            return (
              <BoolRow
                key={s.name}
                label={label}
                value={value === "true"}
                onChange={(v) => setSetting(s.name, v ? "true" : "false")}
              />
            );
          case "select":
            return (
              <SettingsRow
                key={s.name}
                title={label}
                control={
                  <div className="w-56">
                    <SearchSelect
                      value={value}
                      onChange={(v) => setSetting(s.name, v)}
                      options={(s.options ?? []).map((o) => ({ value: o.value, label: o.label }))}
                      ariaLabel={label}
                      searchable={(s.options ?? []).length > 7}
                    />
                  </div>
                }
              />
            );
          case "multi-select":
            return (
              <SettingsRow key={s.name} stacked title={label}>
                <ChipGroup
                  ariaLabel={label}
                  size="sm"
                  options={(s.options ?? []).map((o) => ({ value: o.value, label: o.label }))}
                  value={value ? value.split(",") : []}
                  onChange={(v) => setSetting(s.name, v.join(","))}
                />
              </SettingsRow>
            );
          default:
            return (
              <TextRow
                key={s.name}
                label={label}
                value={value}
                error={errors[`settings.${s.name}`]}
                onChange={(v) => setSetting(s.name, v)}
              />
            );
        }
      })}
    </SettingsSection>
  );
}

/**
 * Pick the site for a new built-in indexer: an installed definition, or one
 * from the owner's definitions source (installed on pick).
 */
export function SitePicker({
  definitions,
  onPick,
  onInstalled,
}: {
  definitions: SiteDefinition[];
  onPick: (def: SiteDefinition) => void;
  onInstalled: () => void;
}) {
  const [query, setQuery] = useState("");
  const [catalog, setCatalog] = useState<CatalogEntry[] | null>(null);
  const [catalogState, setCatalogState] = useState<"idle" | "loading" | "none" | "error">("idle");
  const [catalogError, setCatalogError] = useState<unknown>(null);
  const [installing, setInstalling] = useState<string | null>(null);

  async function loadCatalog(refresh = false) {
    setCatalogState("loading");
    try {
      setCatalog(await api.sites.catalog(refresh));
      setCatalogState("idle");
    } catch (e) {
      if (e instanceof ApiError && e.code === "no_source") setCatalogState("none");
      else {
        setCatalogError(e);
        setCatalogState("error");
      }
    }
  }
  useEffect(() => {
    void loadCatalog();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const q = query.trim().toLowerCase();
  const installed = definitions.filter(
    (d) =>
      !q ||
      d.name.toLowerCase().includes(q) ||
      d.id.toLowerCase().includes(q) ||
      (d.description ?? "").toLowerCase().includes(q),
  );
  const installedIds = new Set(definitions.map((d) => d.id));
  const offered = (catalog ?? []).filter(
    (e) => !e.installed && !installedIds.has(e.id) && (!q || e.id.toLowerCase().includes(q)),
  );

  async function install(id: string) {
    setInstalling(id);
    try {
      const def = await api.sites.install(id);
      toast.success(`${def.name} installed`);
      onInstalled();
      onPick(def);
    } catch (e) {
      toastError(`Couldn't install ${id}`, e);
    } finally {
      setInstalling(null);
    }
  }

  return (
    <div className="space-y-4">
      <Input
        aria-label="Search sites"
        placeholder="Search sites"
        value={query}
        onChange={(e) => setQuery(e.currentTarget.value)}
      />
      <section aria-label="Installed sites" className="space-y-2">
        <h3 className="text-sm font-semibold text-fg">Installed</h3>
        {definitions.length === 0 ? (
          <p className="text-sm text-fg-muted">
            No site definitions installed yet. Upload one under Built-in sites, or pick one from
            your definitions source below.
          </p>
        ) : installed.length === 0 ? (
          <p className="text-sm text-fg-muted">No installed site matches.</p>
        ) : (
          <ul className="divide-y divide-border rounded-xl border border-border">
            {installed.map((d) => (
              <li key={d.id} className="flex flex-wrap items-center gap-3 px-4 py-2.5">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium text-fg">{d.name}</p>
                  {d.description ? (
                    <p className="truncate text-xs text-fg-muted">{d.description}</p>
                  ) : null}
                </div>
                <DefinitionBadges def={d} />
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => onPick(d)}
                  aria-label={`Use ${d.name}`}
                >
                  Use
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>
      <section aria-label="From your definitions source" className="space-y-2">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-sm font-semibold text-fg">From your definitions source</h3>
          {catalogState !== "none" ? (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => void loadCatalog(true)}
              disabled={catalogState === "loading"}
            >
              <RefreshCw size={13} /> Refresh
            </Button>
          ) : null}
        </div>
        {catalogState === "none" ? (
          <p className="text-sm text-fg-muted">
            No definitions source is set. Add one under Built-in sites to browse its definitions
            here.
          </p>
        ) : catalogState === "error" ? (
          <MutationError error={catalogError} />
        ) : catalogState === "loading" ? (
          <LoadingRows rows={2} />
        ) : offered.length === 0 ? (
          <p className="text-sm text-fg-muted">
            {q ? "Nothing in the source matches." : "Everything the source offers is installed."}
          </p>
        ) : (
          <ul className="max-h-80 divide-y divide-border overflow-y-auto rounded-xl border border-border">
            {offered.map((e) => (
              <li key={e.id} className="flex items-center gap-3 px-4 py-2">
                <span className="min-w-0 flex-1 truncate text-sm text-fg">{e.id}</span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={installing !== null}
                  onClick={() => void install(e.id)}
                  aria-label={`Install ${e.id}`}
                >
                  <Download size={13} /> {installing === e.id ? "Installing…" : "Install"}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

/** Upload or paste one definition. */
function UploadDrawer({
  open,
  onClose,
  onInstalled,
}: {
  open: boolean;
  onClose: () => void;
  onInstalled: (def: SiteDefinition) => void;
}) {
  const [yaml, setYaml] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  async function install() {
    setBusy(true);
    setError(null);
    try {
      const def = await api.sites.upload(yaml);
      toast.success(`${def.name} installed`, {
        description: def.warnings?.length ? def.warnings.join(" · ") : undefined,
      });
      setYaml("");
      onInstalled(def);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Drawer
      open={open}
      onClose={() => !busy && onClose()}
      title="Upload definition"
      description="A site definition in the Cardigann YAML format, for a site you have the right to use."
      size="lg"
      footer={
        <div className="flex w-full justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button size="sm" onClick={() => void install()} disabled={busy || !yaml.trim()}>
            <Upload size={14} /> {busy ? "Installing…" : "Install definition"}
          </Button>
        </div>
      }
    >
      <div className="space-y-4">
        {error ? <MutationError error={error} /> : null}
        <SettingsRow stacked title="Definition file">
          <input
            type="file"
            accept=".yml,.yaml,text/yaml"
            aria-label="Definition file"
            className="text-sm text-fg-muted"
            onChange={(e) => {
              const file = e.currentTarget.files?.[0];
              if (file) void file.text().then(setYaml);
            }}
          />
        </SettingsRow>
        <SettingsRow stacked title="Or paste it">
          <Textarea
            aria-label="Definition YAML"
            rows={14}
            className="font-mono text-xs"
            spellCheck={false}
            value={yaml}
            onChange={(e) => setYaml(e.currentTarget.value)}
            placeholder={"id: example\nname: Example\nlinks:\n  - https://example.invalid/\n…"}
          />
        </SettingsRow>
      </div>
    </Drawer>
  );
}

/**
 * Built-in sites, edited in place: where definitions come from (an optional
 * source the owner trusts), FlareSolverr for sites behind a browser
 * challenge, and the installed definitions.
 */
export function BuiltinSites({
  cfg,
  definitions,
  definitionsError,
  inUse,
  onConfigSaved,
  onDefinitionsChanged,
}: {
  cfg: EtherConfig;
  definitions: SiteDefinition[] | null;
  definitionsError: unknown;
  /** Definition ids configured indexers use. */
  inUse: Set<string>;
  onConfigSaved: () => Promise<void> | void;
  onDefinitionsChanged: () => void;
}) {
  const saved = useMemo<IndexerSitesConfig>(
    () => ({
      definitionsUrl: cfg.indexerSites?.definitionsUrl ?? "",
      flareSolverrUrl: cfg.indexerSites?.flareSolverrUrl ?? "",
    }),
    [cfg.indexerSites?.definitionsUrl, cfg.indexerSites?.flareSolverrUrl],
  );
  const { draft, setDraft, dirty, reset } = useDraft<IndexerSitesConfig>(saved);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [uploading, setUploading] = useState(false);
  const [updating, setUpdating] = useState(false);

  async function save() {
    if (!draft) return;
    setSaving(true);
    setError(null);
    setErrors({});
    try {
      await api.putSection("indexerSites", draft);
      toast.success("Settings saved", { description: "Built-in sites" });
      await onConfigSaved();
    } catch (e) {
      setError(e);
      setErrors(recordErrors(e, "indexerSites."));
    } finally {
      setSaving(false);
    }
  }

  async function update() {
    setUpdating(true);
    try {
      const rep = await api.sites.update();
      const parts = [
        `${rep.updated.length} updated`,
        `${rep.unchanged} unchanged`,
        rep.failed.length ? `${rep.failed.length} failed` : "",
      ].filter(Boolean);
      if (rep.failed.length)
        toast.error("Some definitions could not be updated", {
          description: rep.failed.map((f) => `${f.id}: ${f.error}`).join(" · "),
        });
      else toast.success("Definitions updated", { description: parts.join(" · ") });
      onDefinitionsChanged();
    } catch (e) {
      toastError("Couldn't update definitions", e);
    } finally {
      setUpdating(false);
    }
  }

  async function remove(def: SiteDefinition) {
    const ok = await confirmDialog({
      title: "Remove definition?",
      message: `${def.name} will no longer be available for new indexers.`,
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!ok) return;
    try {
      await api.sites.remove(def.id);
      toast.success(`${def.name} removed`);
      onDefinitionsChanged();
    } catch (e) {
      toastError(`Couldn't remove ${def.name}`, e);
    }
  }

  if (!draft) return null;
  const fromSource = (definitions ?? []).some((d) => d.source === "source");
  return (
    <>
      <Section
        title="Built-in sites"
        description="Sites Ether searches itself, using definitions you install. Ether ships none: add only definitions for sites you have the right to use."
        actions={
          <>
            <Button size="sm" variant="outline" onClick={() => setUploading(true)}>
              <Upload size={14} /> Upload definition
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => void update()}
              disabled={updating || !fromSource}
              title={fromSource ? undefined : "No installed definition came from a source"}
            >
              <RefreshCw size={14} /> {updating ? "Updating…" : "Update definitions"}
            </Button>
          </>
        }
        bodyClassName=""
      >
        {definitionsError ? (
          <div className="px-5 py-4">
            <ErrorPanel error={definitionsError} onRetry={onDefinitionsChanged} />
          </div>
        ) : definitions === null ? (
          <div className="px-5 py-4">
            <LoadingRows rows={2} />
          </div>
        ) : definitions.length === 0 ? (
          <EmptyState
            icon={<Globe />}
            title="No site definitions"
            description="Upload a definition, or set a definitions source and pick sites when you add an indexer."
          />
        ) : (
          <ul aria-label="Installed definitions" className="divide-y divide-border">
            {definitions.map((d) => (
              <li key={d.id} className="flex flex-wrap items-center gap-3 px-5 py-3">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium text-fg">{d.name}</p>
                  <p className="text-xs text-fg-muted">
                    {[
                      d.id,
                      d.source === "source" ? "From your source" : "Uploaded",
                      d.login ? "Needs an account" : "",
                      inUse.has(d.id) ? "In use" : "",
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </p>
                  {d.warnings?.length ? (
                    <p className="text-xs text-warning">{d.warnings.join(" · ")}</p>
                  ) : null}
                </div>
                <DefinitionBadges def={d} />
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-danger"
                  disabled={inUse.has(d.id)}
                  title={inUse.has(d.id) ? "Remove the indexers that use it first" : undefined}
                  onClick={() => void remove(d)}
                  aria-label={`Remove ${d.name}`}
                >
                  Remove
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Section>
      <SettingsSection
        title="Definitions and challenges"
        description="Both are optional services you run or trust yourself."
      >
        <TextRow
          label="Definitions source"
          type="url"
          value={draft.definitionsUrl ?? ""}
          placeholder="https://…/definitions/"
          error={errors.definitionsUrl}
          help="A folder of Cardigann-format definitions (a GitHub folder or raw folder URL, a GitHub contents API URL, or a plain list of definition URLs). Ether lists it when you add a site and refreshes installed definitions from it."
          onChange={(v) => setDraft({ ...draft, definitionsUrl: v })}
        />
        <TextRow
          label="FlareSolverr"
          type="url"
          value={draft.flareSolverrUrl ?? ""}
          placeholder="http://localhost:8191"
          error={errors.flareSolverrUrl}
          help="Passes Cloudflare browser challenges for sites that use them. Without it, those sites report that they need FlareSolverr."
          onChange={(v) => setDraft({ ...draft, flareSolverrUrl: v })}
        />
      </SettingsSection>
      <SaveBar
        dirty={dirty}
        saving={saving}
        error={error}
        onSave={save}
        onDiscard={() => {
          reset();
          setError(null);
          setErrors({});
        }}
      />
      <UploadDrawer
        open={uploading}
        onClose={() => setUploading(false)}
        onInstalled={() => {
          setUploading(false);
          onDefinitionsChanged();
        }}
      />
    </>
  );
}

/** Shown in a built-in indexer's drawer when its definition is gone. */
export function MissingDefinition({ id }: { id?: string }) {
  return (
    <Alert variant="warning" role="status">
      <AlertDescription>
        The definition “{id || "unknown"}” is not installed, so this site cannot be searched.
        Install it again under Built-in sites, or remove this indexer.
      </AlertDescription>
    </Alert>
  );
}
