import { Alert, AlertDescription, Button, Card, Input, Label } from "@braedonsaunders/appkit-ui";
import { ChevronRight, KeyRound } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { EtherLogo } from "../components/brand";
import { api, ApiError, errorMessage, getToken, setToken } from "../lib/api";
import type { Bootstrap } from "../lib/types";

const SIGN_IN_TIMEOUT_MS = 10 * 60 * 1000;
const POLL_MS = 2000;

/**
 * Owner sign-in is required on every connection, including localhost.
 */
export default function Login({ boot, onReady }: { boot: Bootstrap; onReady: () => void }) {
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState<{ code: string; authUrl: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const cancelled = useRef(false);
  useEffect(
    () => () => {
      cancelled.current = true;
    },
    [],
  );

  const signIn = async () => {
    setError(null);
    setBusy(true);
    // Open the window inside the click so popup blockers allow it.
    const popup = window.open("", "plex-sign-in", "width=560,height=720");
    try {
      const pin = await api.signInStart();
      setPending({ code: pin.code, authUrl: pin.authUrl });
      if (popup) popup.location.href = pin.authUrl;
      const deadline = Date.now() + SIGN_IN_TIMEOUT_MS;
      while (!cancelled.current && Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, POLL_MS));
        const res = await api.signInPoll(pin.handle);
        if (res.done && res.token) {
          setToken(res.token);
          popup?.close();
          onReady();
          return;
        }
      }
      if (!cancelled.current) setError(new Error("Sign-in timed out. Try again."));
    } catch (err) {
      popup?.close();
      setError(
        err instanceof ApiError && err.code === "not_owner"
          ? new Error(
              "That Plex account doesn't own this server. Sign in with the account that set Ether up.",
            )
          : err,
      );
    } finally {
      if (!cancelled.current) {
        setBusy(false);
        setPending(null);
      }
    }
  };

  return (
    <main className="grid min-h-screen place-items-center bg-bg-subtle p-4">
      <Card className="reveal w-full max-w-sm p-7 shadow-lg">
        <div className="mb-6 flex flex-col items-center text-center">
          <EtherLogo className="w-48" />
          <h1 className="mt-4 text-xl font-semibold text-fg">{boot.serverName || "Ether"}</h1>
          <p className="mt-1 text-sm text-fg-muted">
            Sign in with the Plex account that owns this server.
          </p>
        </div>
        {error ? (
          <Alert variant="destructive" className="mb-4" role="alert">
            <AlertDescription>{errorMessage(error)}</AlertDescription>
          </Alert>
        ) : null}
        {boot.signIn !== false ? (
          <Button
            type="button"
            size="lg"
            className="w-full"
            onClick={() => void signIn()}
            disabled={busy}
          >
            {busy ? "Waiting for Plex…" : "Sign in with Plex"}
          </Button>
        ) : null}
        {pending ? (
          <p className="mt-3 text-center text-xs text-fg-muted">
            Finish signing in in the Plex window. No window?{" "}
            <a
              className="font-medium text-primary underline-offset-4 hover:underline"
              href={pending.authUrl}
              target="_blank"
              rel="noreferrer"
            >
              Open the Plex sign-in page
            </a>
            .
          </p>
        ) : null}
        <TokenFallback onReady={onReady} />
        <p className="mt-6 text-center text-xs text-fg-subtle">
          Your Plex account must own this Ether server.
        </p>
      </Card>
    </main>
  );
}

function TokenFallback({ onReady }: { onReady: () => void }) {
  const [value, setValue] = useState(getToken());
  const [error, setError] = useState<unknown>(null);
  const [checking, setChecking] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setChecking(true);
    setError(null);
    setToken(value.trim());
    try {
      await api.setupStatus(); // cheap authenticated probe
      onReady();
    } catch (err) {
      setToken("");
      setError(
        err instanceof ApiError && err.status === 401
          ? new Error("That token was not accepted as the owner token.")
          : err,
      );
    } finally {
      setChecking(false);
    }
  };
  return (
    <details className="group mt-5 rounded-lg border border-border-subtle px-3 py-2">
      <summary className="flex cursor-pointer list-none items-center gap-1.5 text-xs font-medium text-fg-muted hover:text-fg">
        <ChevronRight size={14} className="transition-transform group-open:rotate-90" aria-hidden />
        Use a Plex token instead
      </summary>
      <form onSubmit={submit} className="mt-3 space-y-3">
        {error ? (
          <p className="text-xs text-danger" role="alert">
            {errorMessage(error)}
          </p>
        ) : null}
        <div className="space-y-1.5">
          <Label htmlFor="owner-token" className="text-xs">
            Owner token (X-Plex-Token)
          </Label>
          <Input
            id="owner-token"
            type="password"
            value={value}
            onChange={(e) => setValue(e.currentTarget.value)}
            autoComplete="off"
          />
        </div>
        <Button
          type="submit"
          variant="outline"
          size="sm"
          className="w-full"
          disabled={checking || !value.trim()}
        >
          <KeyRound size={14} /> {checking ? "Checking…" : "Unlock"}
        </Button>
      </form>
    </details>
  );
}
