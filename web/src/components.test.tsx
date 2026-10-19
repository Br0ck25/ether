import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { JsonView, UsageBar } from "./components";
import { formatBytes } from "./format";

describe("JsonView generic status rendering", () => {
  it("renders nested blobs with humanized keys and byte formatting", () => {
    render(<JsonView value={{ usedBytes: 2 * 1024 ** 3, healthy: true, indexerName: "NZBgeek", remaining: 42 }} />);
    expect(screen.getByText("Used Bytes")).toBeInTheDocument();
    expect(screen.getByText("2.0 GiB")).toBeInTheDocument();
    expect(screen.getByText("NZBgeek")).toBeInTheDocument();
    expect(screen.getByText("Yes")).toBeInTheDocument();
  });
  it("renders lists of objects (e.g. sessions) as items", () => {
    render(<JsonView value={[{ title: "Dune" }, { title: "Severance" }]} />);
    expect(screen.getByText("Dune")).toBeInTheDocument();
    expect(screen.getByText("Severance")).toBeInTheDocument();
  });
  it("handles null and empty arrays without crashing", () => {
    const { rerender } = render(<JsonView value={null} />);
    expect(screen.getByText("—")).toBeInTheDocument();
    rerender(<JsonView value={[]} />);
    expect(screen.getByText("Nothing to show")).toBeInTheDocument();
  });
});

describe("UsageBar + formatBytes", () => {
  it("shows used, budget and percentage", () => {
    render(<UsageBar used={25 * 1024 ** 3} budget={50 * 1024 ** 3} label="Cache" />);
    expect(screen.getByText(/25\.0 GiB of 50\.0 GiB · 50%/)).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "50");
  });
  it("formats byte units", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(-1)).toBe("—");
  });
});
