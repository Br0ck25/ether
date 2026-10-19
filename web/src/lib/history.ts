import { useCallback, useReducer } from "react";

/** Undo/redo history. Rapid edits sharing a coalesce key fold into one step. */
export interface HistoryState<T> {
  past: T[];
  present: T;
  future: T[];
  lastKey: string | null;
  lastAt: number;
}

export type HistoryAction<T> =
  | { type: "set"; value: T; key?: string; at: number }
  | { type: "undo" }
  | { type: "redo" }
  | { type: "reset"; value: T };

export const HISTORY_LIMIT = 100;
export const COALESCE_MS = 800;

export function initHistory<T>(value: T): HistoryState<T> {
  return { past: [], present: value, future: [], lastKey: null, lastAt: 0 };
}

export function historyReducer<T>(state: HistoryState<T>, action: HistoryAction<T>): HistoryState<T> {
  switch (action.type) {
    case "set": {
      if (Object.is(action.value, state.present)) return state;
      const key = action.key ?? null;
      const coalesce = key !== null && key === state.lastKey && action.at - state.lastAt < COALESCE_MS && state.past.length > 0;
      if (coalesce) return { ...state, present: action.value, future: [], lastAt: action.at };
      const past = [...state.past, state.present];
      if (past.length > HISTORY_LIMIT) past.shift();
      return { past, present: action.value, future: [], lastKey: key, lastAt: action.at };
    }
    case "undo": {
      const prev = state.past[state.past.length - 1];
      if (state.past.length === 0) return state;
      return { past: state.past.slice(0, -1), present: prev as T, future: [state.present, ...state.future], lastKey: null, lastAt: 0 };
    }
    case "redo": {
      if (state.future.length === 0) return state;
      const [next, ...rest] = state.future;
      return { past: [...state.past, state.present], present: next as T, future: rest, lastKey: null, lastAt: 0 };
    }
    case "reset":
      return initHistory(action.value);
  }
}

export function useHistory<T>(initial: T) {
  const [state, dispatch] = useReducer(historyReducer<T>, initial, initHistory);
  const set = useCallback((value: T, key?: string) => dispatch({ type: "set", value, key, at: Date.now() }), []);
  const undo = useCallback(() => dispatch({ type: "undo" }), []);
  const redo = useCallback(() => dispatch({ type: "redo" }), []);
  const reset = useCallback((value: T) => dispatch({ type: "reset", value }), []);
  return {
    value: state.present,
    set, undo, redo, reset,
    canUndo: state.past.length > 0,
    canRedo: state.future.length > 0,
  };
}

// ---- Local drafts ------------------------------------------------------------

const DRAFT_PREFIX = "ether.draft.";

export interface StoredDraft<T> {
  value: T;
  savedAt: string;
  /** updatedAt of the server copy the draft was based on. */
  baseVersion?: string;
}

export function draftKey(kind: string, id: string): string {
  return `${DRAFT_PREFIX}${kind}.${id}`;
}

export function loadDraft<T>(key: string): StoredDraft<T> | null {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as StoredDraft<T>;
    return parsed && typeof parsed === "object" && "value" in parsed ? parsed : null;
  } catch {
    return null;
  }
}

export function saveDraft<T>(key: string, value: T, baseVersion?: string): void {
  try {
    const draft: StoredDraft<T> = { value, savedAt: new Date().toISOString(), baseVersion };
    localStorage.setItem(key, JSON.stringify(draft));
  } catch { /* storage full or unavailable: drafts are best effort */ }
}

export function clearDraft(key: string): void {
  try { localStorage.removeItem(key); } catch { /* ignore */ }
}

/** All drafts of a kind, newest first (for "resume where you left off"). */
export function listDrafts<T>(kind: string): { id: string; draft: StoredDraft<T> }[] {
  const out: { id: string; draft: StoredDraft<T> }[] = [];
  try {
    const prefix = `${DRAFT_PREFIX}${kind}.`;
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (!k || !k.startsWith(prefix)) continue;
      const d = loadDraft<T>(k);
      if (d) out.push({ id: k.slice(prefix.length), draft: d });
    }
  } catch { /* ignore */ }
  return out.sort((a, b) => b.draft.savedAt.localeCompare(a.draft.savedAt));
}
