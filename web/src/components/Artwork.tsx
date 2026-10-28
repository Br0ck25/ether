import { cn } from "@braedonsaunders/appkit-ui";
import { Film } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { apiBlob, imageNeedsFetch } from "../lib/api";
import { initials } from "../lib/format";

/**
 * Artwork that loads lazily and works for remote owners too: images under
 * /ether/api need the owner token, which an <img> can't send, so when a
 * token is in use those are fetched with the header and shown from a blob.
 * Everything else (TMDB CDN, plex.tv avatars) is a plain lazy <img>.
 */
export function Artwork({
  src,
  alt,
  title,
  className,
  imgClassName,
  eager,
  icon,
}: {
  src?: string | null;
  alt: string;
  /** Text for the placeholder when there's no image (falls back to alt). */
  title?: string;
  className?: string;
  imgClassName?: string;
  eager?: boolean;
  icon?: React.ReactNode;
}) {
  const needsFetch = imageNeedsFetch(src);
  const [blobUrl, setBlobUrl] = useState<string | null>(null);
  // Load/fail state is keyed by URL so a new src never inherits the old state,
  // and an image that finished loading before React attached its handler
  // (cached or data: URLs) is still revealed.
  const [loadedSrc, setLoadedSrc] = useState<string | null>(null);
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const holder = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!needsFetch || !src) return;
    let url: string | null = null;
    const ctl = new AbortController();
    const load = () => {
      apiBlob(src, ctl.signal)
        .then((b) => {
          url = URL.createObjectURL(b);
          setBlobUrl(url);
        })
        .catch(() => {
          if (!ctl.signal.aborted) setFailedSrc(src);
        });
    };
    const el = holder.current;
    let io: IntersectionObserver | null = null;
    if (eager || !el || typeof IntersectionObserver === "undefined") load();
    else {
      io = new IntersectionObserver(
        (entries) => {
          if (entries.some((e) => e.isIntersecting)) {
            io?.disconnect();
            load();
          }
        },
        { rootMargin: "300px" },
      );
      io.observe(el);
    }
    return () => {
      ctl.abort();
      io?.disconnect();
      if (url) URL.revokeObjectURL(url);
      setBlobUrl(null);
    };
  }, [needsFetch, src, eager]);

  const shown = needsFetch ? blobUrl : (src ?? null);
  const failed = Boolean(src) && failedSrc === src;
  const imgRef = useCallback(
    (img: HTMLImageElement | null) => {
      if (img && shown && img.complete && img.naturalWidth > 0) setLoadedSrc(shown);
    },
    [shown],
  );

  return (
    <div ref={holder} className={cn("relative overflow-hidden bg-bg-subtle", className)}>
      {shown && !failed ? (
        <img
          ref={imgRef}
          src={shown}
          alt={alt}
          loading={eager ? "eager" : "lazy"}
          decoding="async"
          draggable={false}
          data-loaded={loadedSrc === shown ? "true" : "false"}
          onLoad={() => setLoadedSrc(shown)}
          onError={() => setFailedSrc(src ?? null)}
          className={cn("img-fade size-full object-cover", imgClassName)}
        />
      ) : null}
      {!shown || failed ? (
        <div
          className="absolute inset-0 grid place-items-center p-2 text-center"
          aria-hidden={Boolean(alt) || undefined}
        >
          <div className="flex flex-col items-center gap-1.5 text-fg-subtle">
            {icon ?? <Film size={18} />}
            <span className="line-clamp-3 text-xs font-medium text-fg-muted">
              {title ?? (alt ? initials(alt) : "")}
            </span>
          </div>
        </div>
      ) : null}
    </div>
  );
}
