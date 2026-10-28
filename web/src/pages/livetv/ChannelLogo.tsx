import { cn } from "@braedonsaunders/appkit-ui";
import { useState } from "react";
import { initials } from "../../lib/format";
import { hueOf } from "./model";

/**
 * A channel's logo on a neutral tile, or a generated monogram in the
 * channel's own hue when it has none (or the image fails to load).
 */
export function ChannelLogo({
  name,
  logo,
  seed,
  className,
  size = 36,
}: {
  name: string;
  logo?: string;
  seed?: string;
  className?: string;
  size?: number;
}) {
  const [failed, setFailed] = useState<string | null>(null);
  const hue = hueOf(seed || name || "?");
  const showImg = logo && failed !== logo;
  return (
    <span
      className={cn(
        "relative grid shrink-0 place-items-center overflow-hidden rounded-lg ring-1 ring-inset ring-black/5 dark:ring-white/10",
        showImg ? "bg-white dark:bg-white/90" : "",
        className,
      )}
      style={{
        width: size,
        height: size,
        ...(showImg
          ? null
          : {
              background: `linear-gradient(135deg, hsl(${hue} 70% 52%), hsl(${(hue + 40) % 360} 75% 38%))`,
            }),
      }}
      aria-hidden
    >
      {showImg ? (
        <img
          src={logo}
          alt=""
          loading="lazy"
          className="size-full object-contain p-[12%]"
          onError={() => setFailed(logo)}
        />
      ) : (
        <span
          className="font-semibold tracking-tight text-white drop-shadow-sm"
          style={{ fontSize: Math.max(10, Math.round(size * 0.36)) }}
        >
          {initials(name || "?")}
        </span>
      )}
    </span>
  );
}
