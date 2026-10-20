import { Button, SearchSelect, type SelectOption } from "@braedonsaunders/appkit-ui";
import { RefreshCw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, errorMessage, isAbort } from "../lib/api";
import { useDebouncedValue } from "../lib/hooks";
import type { EtherConfig, LLMModel, LLMProvider } from "../lib/types";

type LLMConfig = EtherConfig["llm"];

let providersPromise: Promise<LLMProvider[]> | null = null;

/** The provider catalogue (internal/config/llm.go), fetched once per page load. */
export function useLLMProviders() {
  const [providers, setProviders] = useState<LLMProvider[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let live = true;
    providersPromise ??= api.llmProviders();
    providersPromise.then(
      (p) => {
        if (live) setProviders(p);
      },
      (e) => {
        providersPromise = null;
        if (live) setError(e);
      },
    );
    return () => {
      live = false;
    };
  }, []);
  return { providers, error };
}

/** The provider `cfg` names; an empty provider is a custom endpoint. */
export function findProvider(providers: LLMProvider[] | null, id: string) {
  return providers?.find((p) => p.id === (id || "custom"));
}

/**
 * Switch provider. The key, endpoint and model belong to the old provider,
 * so they are cleared, except that switching back to the saved provider
 * restores its saved values.
 */
export function withProvider(cfg: LLMConfig, saved: LLMConfig | undefined, id: string): LLMConfig {
  if (saved && (saved.provider || "custom") === id)
    return {
      ...cfg,
      provider: id,
      apiKey: saved.apiKey,
      baseUrl: saved.baseUrl,
      model: saved.model,
    };
  return { ...cfg, provider: id, apiKey: "", baseUrl: "", model: "" };
}

export function providerOptions(providers: LLMProvider[] | null): SelectOption[] {
  return (providers ?? []).map((p) => ({
    value: p.id,
    label: p.label,
    group: p.local ? "Self-hosted" : "Hosted",
  }));
}

/**
 * Searchable model select filled from the provider's own model list. It
 * reloads when the provider, key or endpoint changes, and any typed model
 * ID can be used when the list is unavailable or incomplete.
 */
export function ModelPicker({
  cfg,
  provider,
  value,
  onChange,
  id,
  invalid,
}: {
  cfg: LLMConfig;
  provider: LLMProvider | undefined;
  value: string;
  onChange: (model: string) => void;
  id?: string;
  invalid?: boolean;
}) {
  const ready = Boolean(
    provider &&
    (!provider.needsKey || cfg.apiKey) &&
    (!provider.requiresBaseUrl || cfg.baseUrl.trim()),
  );
  const source = useDebouncedValue(
    JSON.stringify([cfg.provider, cfg.apiKey, cfg.baseUrl.trim()]),
    400,
  );
  const [reload, setReload] = useState(0);
  const [list, setList] = useState<{ models: LLMModel[] | null; error?: string }>({
    models: null,
  });
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("");

  useEffect(() => {
    if (!ready) {
      setList({ models: null });
      return;
    }
    const [providerId, apiKey, baseUrl] = JSON.parse(source) as [string, string, string];
    const ac = new AbortController();
    setLoading(true);
    api
      .llmModels({ enabled: true, provider: providerId, apiKey, baseUrl, model: "" }, ac.signal)
      .then(
        (r) => setList({ models: r.models, error: r.error }),
        (e) => {
          if (!isAbort(e)) setList({ models: [], error: errorMessage(e) });
        },
      )
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false);
      });
    return () => ac.abort();
  }, [source, ready, reload]);

  const options = useMemo(() => {
    const out: SelectOption[] = (list.models ?? []).map((m) => ({
      value: m.id,
      label: m.id,
      hint: m.label,
      // OpenRouter-style vendor/model IDs group by vendor.
      group: m.id.includes("/") ? m.id.split("/")[0] : undefined,
    }));
    const has = (id: string) => out.some((o) => o.value === id);
    if (value && !has(value))
      out.unshift({
        value,
        label: value,
        hint: list.models ? "Not in the provider's list" : undefined,
      });
    const typed = query.trim();
    if (typed && !has(typed) && typed !== value)
      out.unshift({ value: typed, label: typed, hint: "Use this model ID" });
    return out;
  }, [list.models, value, query]);

  let status: string | undefined;
  if (!ready)
    status = provider?.requiresBaseUrl
      ? "Enter the endpoint URL to load its models, or type a model ID."
      : "Enter the API key to load models, or type a model ID.";
  else if (list.error) status = `Couldn't load models: ${list.error}`;
  else if (list.models && !list.models.length) status = "No models found. Type a model ID.";

  return (
    <div className="flex gap-2">
      <SearchSelect
        id={id}
        className="min-w-0 flex-1"
        value={value}
        onChange={(v) => {
          setQuery("");
          onChange(v);
        }}
        options={options}
        ariaLabel="Model"
        placeholder={loading ? "Loading models…" : "Choose a model"}
        searchPlaceholder="Search or type a model ID…"
        searchable
        loading={loading}
        searchingLabel="Loading models…"
        statusMessage={status}
        statusTone={list.error ? "error" : "muted"}
        onSearchChange={setQuery}
        noMatchesLabel="No matching models"
        invalid={invalid}
      />
      <Button
        type="button"
        variant="outline"
        size="icon"
        className="shrink-0"
        aria-label="Reload models"
        title="Reload models"
        disabled={!ready || loading}
        onClick={() => setReload((n) => n + 1)}
      >
        <RefreshCw size={16} className={loading ? "animate-spin" : undefined} />
      </Button>
    </div>
  );
}
