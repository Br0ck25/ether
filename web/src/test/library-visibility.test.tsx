import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderAppWith } from "./routes";
import { calls } from "./harness";

describe("Library visibility", () => {
  it("previews country rules locally and saves only the exclusions patch", async () => {
    const exclusions = [
      {
        name: "Country boundary",
        mediaType: "mixed",
        match: "all",
        rules: [{ field: "country", op: "in", value: ["IN"] }],
      },
    ];
    const { fake } = renderAppWith("/catalog/visibility", (method, path, body) => {
      if (method === "GET" && path === "/config/catalog") return { value: { exclusions } };
      if (method === "POST" && path === "/catalog/visibility/preview")
        return {
          total: 55000,
          hidden: (body as { exclusions: unknown[] }).exclusions.length ? 1200 : 0,
          movies: 1000,
          shows: 200,
          unknownCountry: 300,
          items: [],
        };
    });
    await screen.findByText("Your catalogue. Your boundaries.");
    await screen.findByText("1,200");
    expect(screen.getByText("Country is not language")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Delete group 1" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save visibility" })).toBeEnabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save visibility" }));
    await waitFor(() => expect(calls(fake, "PUT", "/config/catalog")).toHaveLength(1));
    expect(calls(fake, "PUT", "/config/catalog")[0]?.body).toEqual({ exclusions: [] });
    expect(calls(fake, "GET", "/catalog/search")).toHaveLength(0);
  });
  it("does not enable an example exclusion or save an incomplete group", async () => {
    const { fake } = renderAppWith("/catalog/visibility", (_method, path) => {
      if (path === "/config/catalog") return { value: {} };
      if (path === "/catalog/visibility/preview")
        return { total: 500, hidden: 0, movies: 0, shows: 0, unknownCountry: 1, items: [] };
    });
    await screen.findByText("No custom exclusions");
    fireEvent.click(screen.getByRole("button", { name: "Add exclusion group" }));
    await screen.findByLabelText("Group 1 name");
    expect(screen.getByRole("button", { name: "Save visibility" })).toBeDisabled();
    expect(calls(fake, "PUT", "/config/catalog")).toHaveLength(0);
  });
});
