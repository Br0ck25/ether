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
    expect(await screen.findByText("Best rows")).toBeInTheDocument();
    // The header health indicator summarises everything.
    expect(screen.getByRole("button", { name: /Server health/ })).toBeInTheDocument();
  });

  it("shows live server instruments: CPU, memory, network, GPU and Ether itself", async () => {
    const { fake } = renderApp("/");
    const server = (await screen.findByRole("heading", { name: "Server" }, WAIT)).closest(
      "section",
    )!;
    const cpu = await within(server).findByRole("group", { name: "CPU" }, WAIT);
    expect(within(cpu).getByText(/12 cores/)).toBeInTheDocument();
    expect(
      within(cpu).getByRole("img", { name: /CPU use over the last 5 minutes/ }),
    ).toBeInTheDocument();
    expect(
      within(within(server).getByRole("group", { name: "Memory" })).getByText(/of 24.0 GiB/),
    ).toBeInTheDocument();
    const net = within(server).getByRole("group", { name: "Network" });
    expect(within(net).getByLabelText("Download")).toBeInTheDocument();
    expect(within(net).getByLabelText("Upload")).toBeInTheDocument();
    expect(within(net).getAllByText(/Mbit\/s/).length).toBeGreaterThanOrEqual(2);
    const gpu = within(server).getByRole("group", { name: "GPU · RTX 4080" });
    expect(within(gpu).getByText("Encode")).toBeInTheDocument();
    expect(within(gpu).getByText("18%")).toBeInTheDocument();
    expect(within(server).getByText(/\d+% CPU · 412\.0 MiB/)).toBeInTheDocument();
    expect(within(server).getByText("videotoolbox")).toBeInTheDocument();
    expect(calls(fake, "GET", "/system/load").length).toBeGreaterThan(0);
  });

  it("says when this hardware reports no GPU", async () => {
    renderApp("/", {}, (fake) => {
      const original = fake.fetch;
      fake.fetch = async (input, init) => {
        const res = await original(input, init);
        if (!String(input).endsWith("/system/load")) return res;
        const body = await res.json();
        body.gpus = [];
        return new Response(JSON.stringify(body), { status: 200 });
      };
    });
    const gpu = await screen.findByRole("group", { name: "GPU" }, WAIT);
    expect(within(gpu).getByText("Not reported by this hardware")).toBeInTheDocument();
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
