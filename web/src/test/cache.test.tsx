import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { calls, renderApp } from "./harness";

const WAIT = { timeout: 4000 };
describe("Cache management", () => {
  it("keeps a preset as a draft until saved and discards it without a request", async () => {
    const { fake } = renderApp("/cache");
    const preset = await screen.findByRole("button", { name: /Encore/ }, WAIT);
    fireEvent.click(preset);
    expect(screen.getByLabelText("Media disk budget")).toHaveValue(250);
    expect(calls(fake, "PUT", "/config/cache")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(screen.getByRole("button", { name: "Save policy" })).toBeDisabled();
    fireEvent.click(preset);
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/cache")).toHaveLength(1));
    expect(fake.state.config.cache.budgetBytes).toBe(250 * 1024 ** 3);
    expect(fake.state.config.cache.mode).toBe("adaptive");
  });
  it("disables speculation controls in no-media mode and saves the off policy", async () => {
    const { fake } = renderApp("/cache");
    fireEvent.change(await screen.findByLabelText("Retention strategy", {}, WAIT), {
      target: { value: "off" },
    });
    expect(screen.getByRole("switch", { name: "Warm detail pages" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(fake.state.config.cache.mode).toBe("off"));
  });
  it("filters retained titles and updates an owner pin", async () => {
    const { fake } = renderApp("/cache");
    expect(await screen.findByText("Arrival", {}, WAIT)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Search cached releases"), {
      target: { value: "Interstellar" },
    });
    expect(screen.queryByText("Arrival")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Pin Interstellar" }));
    await waitFor(() => expect(calls(fake, "POST", "/cache/pin")).toHaveLength(1));
    expect(await screen.findByText("Kept by you")).toBeInTheDocument();
  });
  it("saves a household blend and rolling TV window with explanations", async () => {
    const { fake } = renderApp("/cache");
    fireEvent.click(await screen.findByRole("button", { name: /Cinema node/ }, WAIT));
    expect(screen.getByLabelText("Selection strategy")).toHaveValue("smart");
    expect(screen.getByLabelText("Include in your shelf")).toHaveValue("mixed");
    fireEvent.change(screen.getByLabelText("Include in your shelf"), { target: { value: "tv" } });
    fireEvent.change(screen.getByLabelText("Household influence"), { target: { value: "80" } });
    fireEvent.change(screen.getByLabelText("Episode window per show"), { target: { value: "6" } });
    fireEvent.change(screen.getByLabelText("Learn from"), { target: { value: "all" } });
    expect(calls(fake, "PUT", "/config/cache")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(fake.state.config.cache.preloadEpisodesAhead).toBe(6));
    expect(fake.state.config.cache.preloadContent).toBe("tv");
    expect(fake.state.config.cache.preloadHouseholdWeight).toBe(80);
    expect(fake.state.config.cache.preloadHouseholdScope).toBe("all");
    expect(await screen.findByText(/Severance · S02E04/)).toBeInTheDocument();
    expect(screen.getByText(/next in a household viewing sequence/)).toBeInTheDocument();
  });
});
