import { Link } from "react-router-dom";
import { ConfigSection } from "../../components/ConfigSection";
import { DomainPage, useRouteTab } from "../../components/layout";
import * as S from "../settings/schema";
import { DeviceCapabilities } from "./Devices";
import { HardwareStatus } from "./Hardware";
import { MarkerCoverage } from "./Markers";
import { SubtitleTiming } from "./SubtitleTiming";

const TABS = [
  { key: "transcoding", label: "Transcoding" },
  { key: "devices", label: "Device capabilities" },
  { key: "markers", label: "Markers" },
  { key: "subtitles", label: "Subtitles" },
] as const;

/** How a chosen release reaches the screen: transcoding, caching, markers, subtitles. */
export default function Playback() {
  const tab = useRouteTab(TABS);
  return (
    <DomainPage
      title="Playback"
      description="How Ether adapts each stream to the app playing it."
      base="/playback"
      tabs={TABS}
      active={tab}
    >
      {tab === "transcoding" ? (
        <ConfigSection
          configKey="transcode"
          endpoint="transcode"
          groups={S.TRANSCODE}
          label="Transcoding"
          before={() => <HardwareStatus />}
        />
      ) : null}
      {tab === "devices" ? <DeviceCapabilities /> : null}
      {tab === "markers" ? (
        <ConfigSection
          configKey="markers"
          endpoint="markers"
          groups={S.MARKERS}
          label="Markers"
          after={() => <MarkerCoverage />}
        />
      ) : null}
      {tab === "subtitles" ? (
        <>
          <p className="max-w-3xl text-sm text-fg-muted">
            Missing subtitles are fetched from OpenSubtitles. The account is set up in{" "}
            <Link to="/services" className="text-primary underline">
              Connected services
            </Link>
            .
          </p>
          <SubtitleTiming />
        </>
      ) : null}
    </DomainPage>
  );
}
