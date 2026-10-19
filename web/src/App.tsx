import { Drawer } from "./components/Drawer";
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
  Activity,
  Gauge,
  LayoutTemplate,
  Layers,
  LogOut,
  ScrollText,
  Settings as SettingsIcon,
  Sparkles,
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
  useNavigate,
  useSearchParams,
} from "react-router-dom";
import { AppProvider, useApp } from "./app-context";
import { Brand, BrandSplash, EtherMark } from "./components/brand";
import { ErrorPanel } from "./components/feedback";
import { LEVEL_LABEL, LevelBadge, StatusDot } from "./components/status";
import { api, getToken, setToken } from "./lib/api";
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
const Adoption = lazy(() => import("./pages/Adoption"));
const StreamPreferences = lazy(() => import("./pages/StreamPreferences"));
const PlaybackPolicy = lazy(() => import("./pages/PlaybackPolicy"));
const Insights = lazy(() => import("./pages/Insights"));
const LiveTV = lazy(() => import("./pages/livetv/LiveTV"));
const Discovery = lazy(() => import("./pages/Discovery"));
const Metadata = lazy(() => import("./pages/Metadata"));
const Safety = lazy(() => import("./pages/Safety"));
const Traces = lazy(() => import("./pages/Traces"));

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

function navigation(setupComplete: boolean): SidebarNavGroup[] {
  const items: SidebarNavGroup["items"] = [
    { href: "/", label: "Home", icon: <Gauge size={18} />, exact: true, mobile: true },
    { href: "/collections", label: "Collections", icon: <Layers size={18} />, mobile: true },
    { href: "/designer", label: "Home Designer", icon: <LayoutTemplate size={18} />, mobile: true },
    { href: "/viewers", label: "Viewers", icon: <Users size={18} />, mobile: true },
    { href: "/discovery", label: "Discovery & artwork", icon: <Sparkles size={18} /> },
    { href: "/livetv", label: "Live TV & IPTV", icon: <Activity size={18} /> },
    { href: "/insights", label: "Activity & notifications", icon: <Gauge size={18} /> },
    { href: "/adoption", label: "Libraries & backups", icon: <Layers size={18} /> },
    { href: "/playback", label: "Playback & releases", icon: <Activity size={18} /> },
    { href: "/metadata", label: "Episode ordering", icon: <Layers size={18} /> },
    { href: "/streams", label: "Audio & subtitles", icon: <Activity size={18} /> },
    { href: "/safety", label: "Safety & sharing", icon: <Users size={18} /> },
    { href: "/settings", label: "Settings", icon: <SettingsIcon size={18} /> },
    { href: "/logs", label: "Logs", icon: <ScrollText size={18} /> },
    { href: "/traces", label: "Resolver traces", icon: <Activity size={18} /> },
  ];
  if (!setupComplete)
    items.unshift({
      href: "/setup",
      label: "Finish setup",
      icon: <Sparkles size={18} />,
      mobile: true,
    });
  return [
    {
      label: "Library",
      items: items.filter((item) =>
        [
          "/setup",
          "/",
          "/collections",
          "/designer",
          "/viewers",
          "/discovery",
          "/livetv",
          "/metadata",
        ].includes(item.href),
      ),
    },
    {
      label: "Playback & access",
      items: items.filter((item) => ["/playback", "/streams", "/safety"].includes(item.href)),
    },
    {
      label: "Server",
      items: items.filter((item) =>
        ["/insights", "/adoption", "/settings", "/logs", "/traces"].includes(item.href),
      ),
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
                to={`/settings/${h.settings}`}
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
function CollectionDrawer({ fresh = false }: { fresh?: boolean }) {
  const navigate = useNavigate();
  return (
    <>
      <div aria-hidden="true" inert className="flex min-h-0 flex-1 flex-col">
        <CollectionsGallery />
      </div>
      <Drawer
        open
        onClose={() => navigate("/collections")}
        title="Collection Studio"
        size="2xl"
        initialFullscreen
        bodyClassName="flex min-h-0 flex-1 flex-col overflow-hidden p-0"
      >
        {fresh ? <NewCollection /> : <CollectionEditor />}
      </Drawer>
    </>
  );
}

function NewCollection() {
  const [search] = useSearchParams();
  return <CollectionEditor key={search.get("draft") ?? "new"} />;
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
      headerMiddle={
        <span className="hidden truncate text-sm text-fg-muted lg:block">
          {boot.serverName}
          <span className="text-fg-subtle"> · {versionLabel(boot.version)}</span>
        </span>
      }
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
        <p className="px-1 text-xs text-fg-subtle">Ether {versionLabel(boot.version)}</p>
      }
      sidebarCollapsedFooter={
        <div className="flex justify-center">
          <EtherMark size={20} />
        </div>
      }
      mobileTabCount={4}
    >
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route
            path="/"
            element={boot.setupComplete ? <Home /> : <Navigate to="/setup" replace />}
          />
          <Route path="/setup" element={<Setup />} />
          <Route path="/collections" element={<CollectionsGallery />} />
          <Route path="/collections/new" element={<CollectionDrawer fresh />} />
          <Route path="/collections/:id" element={<CollectionDrawer />} />
          <Route path="/designer" element={<Designer />} />
          <Route path="/viewers" element={<ViewersList />} />
          <Route path="/viewers/:id" element={<ViewerProfile />} />
          <Route path="/settings" element={<Navigate to="/settings/server" replace />} />
          <Route path="/settings/:section" element={<Settings />} />
          <Route path="/insights" element={<Insights />} />
          <Route path="/livetv" element={<LiveTV />} />
          <Route path="/discovery" element={<Discovery />} />
          <Route path="/adoption" element={<Adoption />} />
          <Route path="/playback" element={<PlaybackPolicy />} />
          <Route path="/metadata" element={<Metadata />} />
          <Route path="/streams" element={<StreamPreferences />} />
          <Route path="/safety" element={<Safety />} />
          <Route path="/logs" element={<Logs />} />
          <Route path="/traces" element={<Traces />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Suspense>
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
