import { Button } from "@braedonsaunders/appkit-ui";
import type { ReactNode } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { Images, ListVideo, Star } from "lucide-react";
import { Workspace } from "../../components/workspace";
import "./metadata.css";

const sections = [
  { key: "artwork", label: "Artwork", icon: Images },
  { key: "ratings", label: "Ratings", icon: Star },
  { key: "episodes", label: "Episode order", icon: ListVideo },
];

export function MetadataWorkspace({
  aside,
  main,
  actions,
  fitPreview = false,
}: {
  aside: ReactNode;
  main: ReactNode;
  actions?: ReactNode;
  fitPreview?: boolean;
}) {
  const location = useLocation();
  return (
    <Workspace
      title="Metadata"
      subtitle="Artwork, rating sources and episode presentation."
      actions={
        <>
          {actions}
          <Button
            size="sm"
            variant="ghost"
            className="lg:hidden"
            onClick={() =>
              document
                .querySelector('[aria-label="Metadata preview"]')
                ?.scrollIntoView({ block: "start" })
            }
          >
            Preview ↓
          </Button>
        </>
      }
      asideLabel="Metadata controls"
      mainLabel="Metadata preview"
      mainClassName={`metadata-canvas ${fitPreview ? "metadata-canvas-fit" : ""}`}
      aside={
        <>
          <nav className="metadata-nav" aria-label="Metadata sections">
            {sections.map(({ key, label, icon: Icon }) => (
              <NavLink
                key={key}
                to={`/metadata/${key}`}
                className={({ isActive }) =>
                  `metadata-nav-item ${isActive || (key === "artwork" && location.pathname.endsWith("/metadata")) ? "is-active" : ""}`
                }
              >
                <Icon size={15} />
                <span>{label}</span>
              </NavLink>
            ))}
          </nav>
          <div className="metadata-controls">{aside}</div>
        </>
      }
      main={main}
    />
  );
}
