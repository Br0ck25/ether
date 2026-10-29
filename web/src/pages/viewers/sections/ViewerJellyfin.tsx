import { Button, Input, SettingsSection, Spinner, toast } from "@braedonsaunders/appkit-ui";
import { useState } from "react";
import { Link } from "react-router-dom";
import { ErrorPanel } from "../../../components/feedback";
import { SettingsRow } from "../../../components/SettingsRow";
import { api, apiFetch, errorMessage } from "../../../lib/api";
import { useAsync } from "../../../lib/hooks";

type SignInViewer = {
  id: number;
  username: string;
  title: string;
  isOwner: boolean;
  hasCode: boolean;
  devices: number;
};

function copy(text: string, what: string) {
  void navigator.clipboard
    .writeText(text)
    .then(() => toast.success(`${what} copied`))
    .catch(() => toast.error(`Select and copy the ${what.toLowerCase()}`));
}

/**
 * How one viewer signs in from a Jellyfin app: their Plex user name
 * and an access code set here (viewer profile › Access).
 */
export function ViewerJellyfinSignIn({ viewerId }: { viewerId: number }) {
  const config = useAsync(() => api.getConfig(), []);
  const viewers = useAsync(
    async (signal) =>
      (await apiFetch<{ viewers: SignInViewer[] }>("/jellyfin/viewers", { signal })).viewers,
    [],
  );
  const [shown, setShown] = useState<string | null>(null);
  const [custom, setCustom] = useState("");
  const [busy, setBusy] = useState(false);

  if (viewers.error)
    return <ErrorPanel error={viewers.error} onRetry={() => void viewers.reload()} />;
  if (!viewers.data)
    return (
      <div className="py-6 text-center">
        <Spinner size={18} label="Loading Jellyfin access" />
      </div>
    );
  const me = viewers.data.find((v) => v.id === viewerId);
  if (!me) return null;

  const enabled = config.data?.jellyfin?.enabled ?? false;
  const port = config.data?.jellyfin?.port || 8096;
  const address = `http://${window.location.hostname}:${port}`;

  const run = async (path: string, init: RequestInit, done: string) => {
    setBusy(true);
    try {
      const res = await apiFetch<{ code?: string } | null>(
        `/jellyfin/viewers/${viewerId}/${path}`,
        init,
      );
      setShown(res?.code ?? null);
      setCustom("");
      toast.success(done);
      await viewers.reload();
    } catch (e) {
      toast.error("That didn't work", { description: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };
  const setCode = (code: string) =>
    run("code", { method: "PUT", body: JSON.stringify({ code }) }, "Access code set");

  return (
    <SettingsSection
      title="Jellyfin sign-in"
      description="Lets this viewer use the Jellyfin apps with their own watch history and limits."
    >
      <SettingsRow
        title="How to connect"
        description={
          enabled ? (
            <>
              In a Jellyfin app, add the server <code>{address}</code> and sign in as{" "}
              <strong>{me.username || me.title}</strong> with the access code below.
            </>
          ) : (
            <>
              Jellyfin apps are switched off.{" "}
              <Link to="/server/jellyfin" className="font-medium text-primary hover:underline">
                Turn them on in General
              </Link>
              .
            </>
          )
        }
      />
      <SettingsRow
        title="Access code"
        description={
          me.hasCode
            ? `Set. ${me.devices} ${me.devices === 1 ? "device is" : "devices are"} signed in. Setting a new code signs them all out.`
            : "Not set, so this viewer cannot sign in from these apps."
        }
        control={
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="outline" size="sm" disabled={busy} onClick={() => void setCode("")}>
              {me.hasCode ? "Generate a new code" : "Generate a code"}
            </Button>
            {me.hasCode ? (
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => void run("code", { method: "DELETE" }, "Access code removed")}
              >
                Remove
              </Button>
            ) : null}
          </div>
        }
      />
      {shown ? (
        <SettingsRow
          title="New access code"
          description="Shown once. Give it to the viewer; Ether only keeps a scrambled copy."
          control={
            <div className="flex items-center gap-2">
              <code className="rounded bg-surface-sunken px-2 py-1 text-sm tracking-wider">
                {shown}
              </code>
              <Button variant="outline" size="sm" onClick={() => copy(shown, "Access code")}>
                Copy
              </Button>
            </div>
          }
        />
      ) : null}
      <SettingsRow
        title="Choose your own"
        description="At least 6 characters."
        control={
          <div className="flex gap-2">
            <div className="w-44">
              <Input
                aria-label="Access code"
                type="password"
                autoComplete="new-password"
                value={custom}
                onChange={(e) => setCustom(e.currentTarget.value)}
              />
            </div>
            <Button
              variant="outline"
              size="sm"
              disabled={busy || custom.length < 6}
              onClick={() => void setCode(custom)}
            >
              Set
            </Button>
          </div>
        }
      />
      {me.devices > 0 ? (
        <SettingsRow
          title="Signed-in devices"
          description="Sign this viewer out of every Jellyfin app."
          control={
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void run("signout", { method: "POST", body: "{}" }, "Signed out")}
            >
              Sign out {me.devices} {me.devices === 1 ? "device" : "devices"}
            </Button>
          }
        />
      ) : null}
    </SettingsSection>
  );
}
