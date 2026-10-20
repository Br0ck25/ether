import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };

describe("Overview", () => {
  it("shows now playing, health, activity and best rows", async () => {
    renderApp("/");
    expect(await screen.findByText(/Sam Rivera · Living Room/, {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText("Direct play")).toBeInTheDocument();
    expect(screen.getByText(/% watched/)).toBeInTheDocument();
    const health = screen.getByText("Health").closest("section")!;
    for (const label of [
      "Usenet",
      "Indexers",
      "Cache",
      "Catalogue",
      "Remote access",
      "plex.direct",
    ]) {
      expect(within(health).getByText(label)).toBeInTheDocument();
    }
    expect(
      await screen.findByRole("img", { name: /Plays per day over the last 30 days/ }, WAIT),
    ).toBeInTheDocument();
    expect(screen.getByText("Plays · 30 days")).toBeInTheDocument();
    expect(await screen.findByText("Best-performing rows")).toBeInTheDocument();
    // The header health indicator summarises everything.
    expect(screen.getByRole("button", { name: /Server health/ })).toBeInTheDocument();
  });

  it("lists problems first, each linking to the screen that fixes it", async () => {
    renderApp("/", {}, (fake) => {
      const original = fake.fetch;
      fake.fetch = async (input, init) => {
        const res = await original(input, init);
        if (!String(input).endsWith("/dashboard")) return res;
        const body = await res.json();
        body.sections.providers = { error: "Every provider refused the login" };
        return new Response(JSON.stringify(body), { status: 200 });
      };
    });
    const attention = await screen.findByRole("region", { name: "Needs attention" }, WAIT);
    expect(within(attention).getByText("Every provider refused the login")).toBeInTheDocument();
    expect(within(attention).getByRole("link", { name: "Fix" })).toHaveAttribute(
      "href",
      "/sources/usenet",
    );
  });
});

describe("Updates settings", () => {
  it("shows update status and asks the server to install", async () => {
    const { fake } = renderApp("/updates");
    expect(await screen.findByText(/Version 0\.3\.0/, {}, WAIT)).toBeInTheDocument();
    expect(screen.getByText(/03:00–06:00 local time/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Update now" }));
    expect(
      await screen.findByRole("button", { name: "Install now anyway" }, WAIT),
    ).toBeInTheDocument();
    expect(calls(fake, "POST", "/updates/apply").length).toBe(1);
  });
});
