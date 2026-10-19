import { useCallback, useEffect, useRef, useState } from "react";

export interface AsyncState<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
  /** Re-run the loader; keeps showing the previous data while it runs. */
  reload: () => Promise<void>;
  setData: (next: T | ((prev: T | undefined) => T)) => void;
}

/**
 * Load data on mount (and whenever `deps` change). Stale responses from a
 * superseded run are dropped so fast dependency changes never flash old data.
 */
export function useAsync<T>(loader: (signal: AbortSignal) => Promise<T>, deps: unknown[]): AsyncState<T> {
  const [data, setDataState] = useState<T | undefined>(undefined);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const runId = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const loaderRef = useRef(loader);
  loaderRef.current = loader;

  const run = useCallback(async () => {
    const id = ++runId.current;
    controller.current?.abort();
    const ctl = new AbortController();
    controller.current = ctl;
    setLoading(true);
    try {
      const value = await loaderRef.current(ctl.signal);
      if (id !== runId.current) return;
      setDataState(value);
      setError(null);
    } catch (e) {
      if (id !== runId.current || ctl.signal.aborted) return;
      setError(e);
    } finally {
      if (id === runId.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void run();
    return () => controller.current?.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  const setData = useCallback((next: T | ((prev: T | undefined) => T)) => {
    setDataState((prev) => (typeof next === "function" ? (next as (p: T | undefined) => T)(prev) : next));
  }, []);

  return { data, error, loading, reload: run, setData };
}

/** Re-run `fn` every `ms` while the tab is visible. */
export function useInterval(fn: () => void, ms: number | null): void {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    if (ms === null) return;
    const tick = () => { if (document.visibilityState !== "hidden") ref.current(); };
    const t = window.setInterval(tick, ms);
    return () => window.clearInterval(t);
  }, [ms]);
}

export function useDebouncedValue<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(value), ms);
    return () => window.clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

/** Media query as state (for compact layouts on phones). */
export function useMediaQuery(query: string): boolean {
  const get = () => (typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia(query).matches : false);
  const [matches, setMatches] = useState(get);
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const m = window.matchMedia(query);
    const on = () => setMatches(m.matches);
    on();
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, [query]);
  return matches;
}
