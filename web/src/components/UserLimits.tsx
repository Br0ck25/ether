import { SettingsRow } from "./SettingsRow";
import { SearchSelect } from "@braedonsaunders/appkit-ui";
import type { ReactNode } from "react";
import type { EtherUser } from "../lib/types";
import { NumberInput } from "./fields";

export const CONTENT_RATINGS = [
  { value: "", label: "Unrestricted" },
  { value: "G", label: "G" },
  { value: "PG", label: "PG" },
  { value: "PG-13", label: "PG-13" },
  { value: "R", label: "R" },
  { value: "NC-17", label: "NC-17" },
  { value: "TV-Y", label: "TV-Y" },
  { value: "TV-Y7", label: "TV-Y7" },
  { value: "TV-G", label: "TV-G" },
  { value: "TV-PG", label: "TV-PG" },
  { value: "TV-14", label: "TV-14" },
  { value: "TV-MA", label: "TV-MA" },
];

/** The user-record fields a viewer's limits edit. */
export type UserLimits = Pick<
  EtherUser,
  "streamLimit" | "maxContentRating" | "subtitleLanguages" | "audioLanguages"
>;

export function userLimits(u: EtherUser): UserLimits {
  return {
    streamLimit: u.streamLimit,
    maxContentRating: u.maxContentRating,
    subtitleLanguages: u.subtitleLanguages,
    audioLanguages: u.audioLanguages,
  };
}

/** Controlled stream-limit and rating-cap rows, for a form saved elsewhere. */
export function UserLimitRows({
  value,
  onChange,
  isOwner,
  defaultStreamLimit,
  streamLimitHint,
}: {
  value: UserLimits;
  onChange: (next: UserLimits) => void;
  isOwner?: boolean;
  defaultStreamLimit?: number;
  /** Extra copy after the stream-limit description (e.g. a settings link). */
  streamLimitHint?: ReactNode;
}) {
  return (
    <>
      <SettingsRow
        title="Stream limit"
        description={
          <>
            Simultaneous streams. 0 uses the server default
            {defaultStreamLimit !== undefined ? ` (${defaultStreamLimit})` : ""}.
            {streamLimitHint ? <> {streamLimitHint}</> : null}
          </>
        }
        control={
          <NumberInput
            aria-label="Stream limit"
            className="w-24"
            value={value.streamLimit}
            min={0}
            onChange={(n) => onChange({ ...value, streamLimit: Math.max(0, Math.round(n)) })}
          />
        }
      />
      <SettingsRow
        title="Content rating cap"
        description={
          isOwner
            ? "The owner is never limited by content rating."
            : "Titles above this rating are hidden from this viewer."
        }
        control={
          <div className="w-44">
            <SearchSelect
              value={value.maxContentRating}
              onChange={(v) => onChange({ ...value, maxContentRating: v })}
              options={CONTENT_RATINGS}
              ariaLabel="Content rating cap"
              searchable={false}
            />
          </div>
        }
      />
    </>
  );
}
