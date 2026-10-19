import { cn } from "@braedonsaunders/appkit-ui";
import { Film } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { apiBlob, imageNeedsFetch } from "../lib/api";
import { initials } from "../lib/format";

/**
 * Artwork that loads lazily and works for remote owners too: images under
 * /ether/api need the owner token, which an <img> can't send, so when a
 * token is in use those are fetched with the header and shown from a blob.
 * Everything else (TMDB CDN, plex.tv avatars) is a plain lazy <img>.
 */
export function Artwork({ src, alt, title, className, imgClassName, eager, icon }: {
  src?: string | null;
  alt: string;
  /** Text for the placeholder when there's no image (falls back to alt). */
  title?: string;
  className?: string;
  imgClassName?: string;
  eager?: boolean;
  icon?: React.ReactNode;
}) {
  const needsAuth = imageNeedsFetch(src);
  const [blobUrl, setBlobUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const holder = useRef<HTMLDivElement>(null);

  useEffect(() => { setFailed(false); setLoaded(false); }, [src]);

  useEffect(() => {
    if (!needsAuth || !src) return;
    let url: string | null = null;
    const ctl = new AbortController();
    const load = () => {
      apiBlob(src, ctl.signal)
        .then((b) => { url = URL.createObjectURL(b); setBlobUrl(url); })
        .catch(() => { if (!ctl.signal.aborted) setFailed(true); });
    };
    const el = holder.current;
    let io: IntersectionObserver | null = null;
    if (eager || !el || typeof IntersectionObserver === "undefined") load();
    else {
      io = new IntersectionObserver((entries) => {
        if (entries.some((e) => e.isIntersecting)) { io?.disconnect(); load(); }
      }, { rootMargin: "300px" });
      io.observe(el);
    }
    return () => { ctl.abort(); io?.disconnect(); if (url) URL.revokeObjectURL(url); setBlobUrl(null); };
  }, [needsAuth, src, eager]);

  const shown = needsAuth ? blobUrl : src;
  return (
    <div ref={holder} className={cn("relative overflow-hidden bg-bg-subtle", className)}>
      {shown && !failed ? (
        <img
          src={shown}
          alt={alt}
          loading={eager ? "eager" : "lazy"}
          decoding="async"
          draggable={false}
          data-loaded={loaded ? "true" : "false"}
          onLoad={() => setLoaded(true)}
          onError={() => setFailed(true)}
          className={cn("img-fade size-full object-cover", imgClassName)}
        />
      ) : null}
      {!shown || failed ? (
        <div className="absolute inset-0 grid place-items-center p-2 text-center" aria-hidden={Boolean(alt) || undefined}>
          <div className="flex flex-col items-center gap-1.5 text-fg-subtle">
            {icon ?? <Film size={18} />}
            <span className="line-clamp-3 text-xs font-medium text-fg-muted">{title ?? (alt ? initials(alt) : "")}</span>
          </div>
        </div>
      ) : null}
    </div>
  );
}
