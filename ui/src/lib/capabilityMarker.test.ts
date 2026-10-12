import { describe, it, expect } from "vitest";
import { isAutoMarkerLine, parseAutoMarkerLine } from "./capabilityMarker";

describe("isAutoMarkerLine", () => {
  it("detects marker lines at the start of a line", () => {
    expect(
      isAutoMarkerLine("# llama-swap auto (2026-02-14) in=[text,image] out=[text] tools=true"),
    ).toBe(true);
  });

  it("detects indented marker lines", () => {
    expect(
      isAutoMarkerLine("    # llama-swap auto (2026-02-14) context=262144"),
    ).toBe(true);
    expect(isAutoMarkerLine("#  llama-swap auto (2026-02-14) in=[text]")).toBe(true);
  });

  it("ignores ordinary comments", () => {
    expect(isAutoMarkerLine("# hand-tuned context")).toBe(false);
    expect(isAutoMarkerLine("    # user note about the model")).toBe(false);
    expect(isAutoMarkerLine("capabilities:")).toBe(false);
    expect(isAutoMarkerLine("")).toBe(false);
  });

  it("ignores non-comment text that merely contains the prefix", () => {
    // A marker always sits in a comment; the value line never matches.
    expect(isAutoMarkerLine("llama-swap auto (2026-02-14) in=[text]")).toBe(false);
  });
});

describe("parseAutoMarkerLine", () => {
  it("returns null for non-marker lines", () => {
    expect(parseAutoMarkerLine("# hand-tuned context")).toBeNull();
    expect(parseAutoMarkerLine("")).toBeNull();
  });

  it("parses the date and every field name", () => {
    const parsed = parseAutoMarkerLine(
      "    # llama-swap auto (2026-02-14) in=[text,image] out=[text] tools=true context=262144",
    );
    expect(parsed).not.toBeNull();
    expect(parsed?.date).toBe("2026-02-14");
    expect(parsed?.fields).toEqual(["in", "out", "tools", "context"]);
  });

  it("handles a marker without a date", () => {
    const parsed = parseAutoMarkerLine("# llama-swap auto in=[text] tools=true");
    expect(parsed).not.toBeNull();
    expect(parsed?.date).toBeUndefined();
    expect(parsed?.fields).toEqual(["in", "tools"]);
  });

  it("handles a single field", () => {
    const parsed = parseAutoMarkerLine("# llama-swap auto (2026-02-14) reranker=true");
    expect(parsed?.fields).toEqual(["reranker"]);
  });
});
