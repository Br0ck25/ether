import { Button } from "@braedonsaunders/appkit-ui";
import { RotateCcw, Save } from "lucide-react";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { MutationError } from "./feedback";

/**
 * Draft state for a singleton form edited in place. `dirty` compares by value,
 * so typing a field back to its saved value clears the save bar.
 */
export function useDraft<T>(saved: T | null | undefined) {
  const [draft, setDraft] = useState<T | null>(saved ?? null);
  const savedKey = useMemo(() => JSON.stringify(saved ?? null), [saved]);
  useEffect(() => {
    setDraft(saved == null ? null : structuredClone(saved));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedKey]);
  const dirty = draft !== null && JSON.stringify(draft) !== savedKey;
  const reset = useCallback(
    () => setDraft(saved == null ? null : structuredClone(saved)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [savedKey],
  );
  return { draft, setDraft, dirty, reset };
}

/** Warn before a reload or tab close while edits are unsaved. */
export function useUnsavedGuard(dirty: boolean) {
  useEffect(() => {
    if (!dirty) return;
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [dirty]);
}

export interface SaveBarProps {
  dirty: boolean;
  saving?: boolean;
  onSave: () => void | Promise<void>;
  onDiscard: () => void;
  error?: unknown;
  saveLabel?: string;
  message?: string;
  disabled?: boolean;
}

type Registry = {
  set: (id: string, props: SaveBarProps | null) => void;
};

const SaveBarContext = createContext<Registry | null>(null);

/**
 * Collects every in-place form on the page into one bar: several independent
 * forms (each saving to its own endpoint) still show a single Save/Discard.
 */
export function SaveBarHost({ children }: { children: ReactNode }) {
  const [forms, setForms] = useState<Record<string, SaveBarProps>>({});
  const registry = useMemo<Registry>(
    () => ({
      set: (id, props) =>
        setForms((all) => {
          if (!props) {
            if (!(id in all)) return all;
            const next = { ...all };
            delete next[id];
            return next;
          }
          return { ...all, [id]: props };
        }),
    }),
    [],
  );
  const list = Object.values(forms);
  const dirty = list.filter((f) => f.dirty);
  const errors = list.filter((f) => f.error).map((f) => f.error);
  const single = dirty.length === 1 ? dirty[0]! : null;
  return (
    <SaveBarContext.Provider value={registry}>
      {children}
      <SaveBarView
        dirty={dirty.length > 0}
        saving={list.some((f) => f.saving)}
        disabled={dirty.some((f) => f.disabled)}
        error={errors[0]}
        message={
          single?.message ??
          (dirty.length > 1 ? `${dirty.length} sections have unsaved changes` : undefined)
        }
        saveLabel={single?.saveLabel}
        onSave={async () => {
          for (const f of dirty) await f.onSave();
        }}
        onDiscard={() => dirty.forEach((f) => f.onDiscard())}
      />
    </SaveBarContext.Provider>
  );
}

/**
 * The one save affordance for in-place forms: it appears only while there are
 * unsaved changes. Inside a `SaveBarHost` it merges into the page's bar.
 */
export function SaveBar(props: SaveBarProps) {
  const registry = useContext(SaveBarContext);
  const id = useId();
  const latest = useRef(props);
  latest.current = props;
  const { dirty, saving, error, message, saveLabel, disabled } = props;
  useEffect(() => {
    if (!registry) return;
    registry.set(id, {
      dirty,
      saving,
      error,
      message,
      saveLabel,
      disabled,
      onSave: () => latest.current.onSave(),
      onDiscard: () => latest.current.onDiscard(),
    });
  }, [registry, id, dirty, saving, error, message, saveLabel, disabled]);
  useEffect(() => () => registry?.set(id, null), [registry, id]);
  if (registry) return null;
  return <SaveBarView {...props} />;
}

function SaveBarView({
  dirty,
  saving = false,
  onSave,
  onDiscard,
  error,
  saveLabel = "Save changes",
  message = "You have unsaved changes",
  disabled = false,
}: SaveBarProps) {
  useUnsavedGuard(dirty);
  if (!dirty && !error) return null;
  return (
    <div
      role="region"
      aria-label="Unsaved changes"
      className="fixed inset-x-3 bottom-20 z-40 mx-auto max-w-2xl rounded-xl border border-border bg-elevated px-4 py-3 shadow-lg md:bottom-5"
    >
      {error ? (
        <div className="mb-3">
          <MutationError error={error} />
        </div>
      ) : null}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-fg-muted">{dirty ? message : "Changes not saved"}</p>
        <div className="flex gap-2">
          <Button variant="ghost" size="sm" onClick={onDiscard} disabled={!dirty || saving}>
            <RotateCcw size={14} /> Discard
          </Button>
          <Button size="sm" onClick={() => void onSave()} disabled={!dirty || saving || disabled}>
            <Save size={14} /> {saving ? "Saving…" : saveLabel}
          </Button>
        </div>
      </div>
    </div>
  );
}
