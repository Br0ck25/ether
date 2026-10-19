import {
  AppShell,
  Button,
  ConfirmRoot,
  Popover,
  PromptRoot,
  Spinner,
  ThemeProvider,
  ThemeToggle,
  Toaster,
  UiLinkProvider,
  type LinkRender,
  type SidebarNavGroup,
} from "@braedonsaunders/appkit-ui";
import {
  Activity as ActivityIcon,
  Bell,
  Gauge,
  LayoutTemplate,
  Layers,
  ListFilter,
  LogOut,
  MonitorPlay,
  Network,
  ScrollText,
  Settings as SettingsIcon,
  Sparkles,
  Tags,
  Tv,
  Users,
} from "lucide-react";
import {
  forwardRef,
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useState,
  type AnchorHTMLAttributes,
} from "react";
import {
  Link,
  Navigate,
  Route,
  Routes,
  useLocation,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { AppProvider, useApp } from "./app-context";
import { Brand, BrandSplash, EtherMark } from "./components/brand";
import { CommandSearch } from "./components/CommandSearch";
import { ErrorPanel } from "./components/feedback";
import { SaveBarHost } from "./components/SaveBar";
import { LEVEL_LABEL, LevelBadge, StatusDot } from "./components/status";
import { api, getToken, setToken } from "./lib/api";
import { legacyRedirect } from "./lib/sitemap";
import type { Bootstrap } from "./lib/types";
import { theme } from "./theme";
import Home from "./pages/Home";
import Login from "./pages/Login";

const CollectionsGallery = lazy(() => import("./pages/collections/Gallery"));
const CollectionEditor = lazy(() => import("./pages/collections/Editor"));
const Designer = lazy(() => import("./pages/designer/Designer"));
const ViewersList = lazy(() => import("./pages/viewers/ViewersList"));
const ViewerProfile = lazy(() => import("./pages/viewers/ViewerProfile"));
const Settings = lazy(() => import("./pages/settings/Settings"));
const Setup = lazy(() => import("./pages/Setup"));
const Logs = lazy(() => import("./pages/Logs"));
const Activity = lazy(() => import("./pages/activity/Activity"));
const Notifications = lazy(() => import("./pages/notifications/Notifications"));
const LiveTV = lazy(() => import("./pages/livetv/LiveTV"));
const Metadata = lazy(() => import("./pages/metadata/Metadata"));
const Sources = lazy(() => import("./pages/sources/Sources"));
const Releases = lazy(() => import("./pages/releases/Releases"));
const Playback = lazy(() => import("./pages/playback/Playback"));

/** react-router links for every AppKit navigation primitive. */
const RouterAnchor = forwardRef<
  HTMLAnchorElement,
  AnchorHTMLAttributes<HTMLAnchorElement> & { href: string }
>(function RouterAnchor({ href, ...rest }, ref) {
  if (/^(https?:)?\/\//.test(href)) return <a ref={ref} href={href} {...rest} />;
  return <Link ref={ref} to={href} {...rest} />;
});

const linkRender: LinkRender = ({ href, children, className, title, ariaCurrent, role }) => (
  <Link to={href} className={className} title={title} aria-current={ariaCurrent} role={role}>
    {children}
  </Link>
);

/**
 * Grouped by the owner's questions: what's happening and who watches; what
 * viewers see in Plex; how titles reach the screen; how the server runs.
 */
function navigation(setupComplete: boolean): SidebarNavGroup[] {
  const icon = 18;
  return [
    {
      label: "",
      items: [
        ...(setupComplete
          ? []
          : [
              {
                href: "/setup",
                label: "Finish setup",
                icon: <Sparkles size={icon} />,
                mobile: true,
              },
            ]),
        { href: "/", label: "Overview", icon: <Gauge size={icon} />, exact: true, mobile: true },
        { href: "/activity", label: "Activity", icon: <ActivityIcon size={icon} />, mobile: true },
        { href: "/viewers", label: "Viewers", icon: <Users size={icon} />, mobile: true },
      ],
    },
    {
      label: "In Plex",
      items: [
        { href: "/home-screens", label: "Home screens", icon: <LayoutTemplate size={icon} /> },
        { href: "/collections", label: "Collections", icon: <Layers size={icon} />, mobile: true },
        { href: "/livetv", label: "Live TV", icon: <Tv size={icon} /> },
        { href: "/metadata", label: "Metadata", icon: <Tags size={icon} /> },
      ],
    },
    {
      label: "Streaming",
      items: [
        { href: "/sources", label: "Sources", icon: <Network size={icon} /> },
        { href: "/releases", label: "Releases", icon: <ListFilter size={icon} /> },
        { href: "/playback", label: "Playback", icon: <MonitorPlay size={icon} /> },
      ],
    },
    {
      label: "Server",
      items: [
        { href: "/notifications", label: "Notifications", icon: <Bell size={icon} /> },
        { href: "/settings", label: "Settings", icon: <SettingsIcon size={icon} /> },
        { href: "/logs", label: "Logs", icon: <ScrollText size={icon} /> },
      ],
    },
  ];
}

function HealthIndicator() {
  const { health, overall, dashboard, dashboardError } = useApp();
  const [open, setOpen] = useState(false);
  const label =
    overall === "ok" || overall === "off"
      ? "All systems normal"
      : overall === "unknown"
        ? "Checking…"
        : LEVEL_LABEL[overall];
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      trigger={
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-label={`Server health: ${label}`}
          className="inline-flex h-9 items-center gap-2 rounded-md px-2.5 text-sm text-fg-muted transition-colors hover:bg-surface-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <StatusDot level={overall === "off" ? "ok" : overall} pulse />
          <span className="hidden md:inline">{label}</span>
        </button>
      }
    >
      <div className="w-80 p-1">
        <p className="px-2 pb-2 pt-1 text-xs font-semibold uppercase tracking-wider text-fg-subtle">
          Server health
        </p>
        {dashboardError && !dashboard ? (
          <p className="px-2 pb-2 text-sm text-danger">Status is unavailable right now.</p>
        ) : null}
        <ul className="space-y-0.5">
          {health.map((h) => (
            <li key={h.id}>
              <Link
                to={h.href}
                onClick={() => setOpen(false)}
                className="flex items-start gap-2.5 rounded-md px-2 py-1.5 hover:bg-surface-hover"
              >
                <StatusDot level={h.level} className="mt-1.5" />
                <span className="min-w-0 flex-1">
                  <span className="flex items-center justify-between gap-2 text-sm font-medium text-fg">
                    {h.label}
                    <LevelBadge level={h.level} />
                  </span>
                  <span className="block truncate text-xs text-fg-muted">{h.summary}</span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </Popover>
  );
}

/** "v1.2.3" for releases, the raw string ("dev") otherwise. */
function versionLabel(v: string): string {
  return /^\d/.test(v) ? `v${v}` : v;
}

/** A fresh editor per new-collection draft (each navigation carries its own draft id). */
function NewCollection() {
  const [search] = useSearchParams();
  return <CollectionEditor key={search.get("draft") ?? "new"} />;
}

/** Pre-rework URLs land on the section's new home. */
function Legacy({ fallback }: { fallback: string }) {
  const location = useLocation();
  return <Navigate to={legacyRedirect(location.pathname, location.search) ?? fallback} replace />;
}

/** `/playback?tab=rules` was the old playback-policy page; the path now belongs to Playback. */
function PlaybackRoute() {
  const location = useLocation();
  const to = legacyRedirect(location.pathname, location.search);
  return to ? <Navigate to={to} replace /> : <Playback />;
}

/** `/settings/usenet` etc. moved to their domain pages; current sections render Settings. */
function SettingsRoute() {
  const { section } = useParams();
  const to = legacyRedirect(`/settings/${section}`, "");
  return to ? <Navigate to={to} replace /> : <Settings />;
}

function PageFallback() {
  return (
    <div className="grid flex-1 place-items-center p-10">
      <Spinner size={22} label="Loading" />
    </div>
  );
}

function Shell() {
  const { boot } = useApp();
  const location = useLocation();
  const remoteSession = boot.authRequired && Boolean(getToken());
  return (
    <AppShell
      groups={navigation(boot.setupComplete)}
      pathname={location.pathname}
      navigationMode="sidebar"
      linkRender={linkRender}
      brand={
        <Link
          to="/"
          aria-label="Ether home"
          className="block rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <Brand serverName={boot.serverName} />
        </Link>
      }
      headerMiddle={<CommandSearch />}
      header={
        <>
          <HealthIndicator />
          <ThemeToggle collapsed />
          {remoteSession ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setToken("");
                window.location.reload();
              }}
            >
              <LogOut size={15} /> <span className="hidden sm:inline">Sign out</span>
            </Button>
          ) : null}
        </>
      }
      sidebarFooter={
        <p className="truncate px-1 text-xs text-fg-subtle" title={boot.serverName}>
          {boot.serverName ? `${boot.serverName} · ` : "Ether "}
          {versionLabel(boot.version)}
        </p>
      }
      sidebarCollapsedFooter={
        <div className="flex justify-center">
          <EtherMark size={20} />
        </div>
      }
      mobileTabCount={4}
    >
      <SaveBarHost>
        <Suspense fallback={<PageFallback />}>
          <Routes>
            <Route
              path="/"
              element={boot.setupComplete ? <Home /> : <Navigate to="/setup" replace />}
            />
            <Route path="/setup" element={<Setup />} />
            <Route path="/activity/:tab?" element={<Activity />} />
            <Route path="/viewers" element={<ViewersList />} />
            <Route path="/viewers/people" element={<ViewersList tab="people" />} />
            <Route path="/viewers/invitations" element={<ViewersList tab="invitations" />} />
            <Route path="/viewers/defaults" element={<ViewersList tab="defaults" />} />
            <Route path="/viewers/:id/:tab?" element={<ViewerProfile />} />
            <Route path="/home-screens" element={<Designer />} />
            <Route path="/collections" element={<CollectionsGallery />} />
            <Route path="/collections/new" element={<NewCollection />} />
            <Route path="/collections/:id" element={<CollectionEditor />} />
            <Route path="/livetv/:tab?" element={<LiveTV />} />
            <Route path="/metadata/:tab?" element={<Metadata />} />
            <Route path="/sources/:tab?" element={<Sources />} />
            <Route path="/releases/:tab?" element={<Releases />} />
            <Route path="/playback" element={<PlaybackRoute />} />
            <Route path="/playback/:tab" element={<Playback />} />
            <Route path="/notifications/:tab?" element={<Notifications />} />
            <Route path="/settings" element={<Navigate to="/settings/server" replace />} />
            <Route path="/settings/:section" element={<SettingsRoute />} />
            <Route path="/logs" element={<Logs />} />
            {[
              "/discovery",
              "/insights",
              "/adoption",
              "/streams",
              "/safety",
              "/traces",
              "/designer",
            ].map((path) => (
              <Route key={path} path={path} element={<Legacy fallback="/" />} />
            ))}
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </SaveBarHost>
    </AppShell>
  );
}

function Gate({ onReady }: { onReady: () => void }) {
  const [boot, setBoot] = useState<Bootstrap | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [unlocked, setUnlocked] = useState(false);

  const loadBoot = useCallback(async () => {
    try {
      setBoot(await api.bootstrap());
      setError(null);
    } catch (e) {
      setError(e);
    }
  }, []);
  useEffect(() => {
    void loadBoot();
  }, [loadBoot]);
  useEffect(() => {
    if (boot || error) onReady();
  }, [boot, error, onReady]);

  if (!boot) {
    return (
      <main className="grid min-h-screen place-items-center bg-bg-subtle p-4">
        {error ? (
          <div className="w-full max-w-md">
            <ErrorPanel error={error} onRetry={() => void loadBoot()} />
          </div>
        ) : (
          <div className="flex flex-col items-center gap-3 text-sm text-fg-muted">
            <EtherMark size={40} motion="loop" />
            <span role="status">Connecting to Ether…</span>
          </div>
        )}
      </main>
    );
  }
  if (boot.authRequired && !boot.authenticated && !unlocked) {
    return (
      <Login
        boot={boot}
        onReady={() => {
          setUnlocked(true);
          void loadBoot();
        }}
      />
    );
  }
  return (
    <AppProvider boot={boot} reloadBoot={loadBoot}>
      <Shell />
    </AppProvider>
  );
}

export default function App() {
  const [ready, setReady] = useState(false);
  const onReady = useCallback(() => setReady(true), []);
  return (
    <ThemeProvider {...theme}>
      <UiLinkProvider link={RouterAnchor}>
        <Gate onReady={onReady} />
        <BrandSplash ready={ready} />
        <Toaster richColors closeButton position="bottom-right" />
        <ConfirmRoot />
        <PromptRoot />
      </UiLinkProvider>
    </ThemeProvider>
  );
}
