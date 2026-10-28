import { describe, expect, it } from "vitest";
import { remoteHealth } from "../lib/health";

const claimed = {
  claimed: true,
  ownerUsername: "owner",
  presence: { connected: true },
  remoteAccess: true,
};

describe("remoteHealth", () => {
  it("is an error when plex.tv rejects the server token", () => {
    const h = remoteHealth({
      ...claimed,
      tokenRejected: true,
      lastPublishError: "401 Invalid authentication token",
    });
    expect(h.level).toBe("error");
    expect(h.summary).toMatch(/rejects/);
    expect(h.details.join(" ")).toMatch(/Authorized Devices/);
  });

  it("warns while publishing or friend sync keeps failing", () => {
    expect(remoteHealth({ ...claimed, lastPublishError: "timeout" }).level).toBe("warn");
    expect(remoteHealth({ ...claimed, friendSyncError: "500" }).summary).toMatch(/Shared users/);
  });

  it("stays ok when plex.tv accepts the server", () => {
    expect(remoteHealth(claimed).level).toBe("ok");
  });
});
