import { render, screen } from "@testing-library/react";
import { useLayoutEffect, useRef } from "react";
import { describe, expect, it } from "vitest";
import { useDraft } from "../components/SaveBar";

// An edit made right after the form first appears (here a layout effect,
// which runs before passive effects, like a fast click) must survive.
function Form({ saved }: { saved: { cachedOnly: boolean } }) {
  const { draft, setDraft, dirty } = useDraft(saved);
  const edited = useRef(false);
  useLayoutEffect(() => {
    if (!edited.current && draft) {
      edited.current = true;
      setDraft({ ...draft, cachedOnly: !draft.cachedOnly });
    }
  });
  return <output>{`${String(draft?.cachedOnly)} ${dirty ? "dirty" : "clean"}`}</output>;
}

describe("useDraft", () => {
  it("keeps an edit made before passive effects run", async () => {
    render(<Form saved={{ cachedOnly: true }} />);
    expect(await screen.findByText("false dirty")).toBeInTheDocument();
  });

  it("adopts a new saved value and drops the old draft", () => {
    const { rerender } = render(<Form saved={{ cachedOnly: true }} />);
    rerender(<Form saved={{ cachedOnly: false }} />);
    expect(screen.getByText("false clean")).toBeInTheDocument();
  });
});
