import { describe, it, expect } from "vitest";
import { isValidModelID } from "./modelId";

describe("isValidModelID", () => {
  it("accepts typical model IDs", () => {
    expect(isValidModelID("my-model-c32768")).toBe(true);
    expect(isValidModelID("qwen3-30b-a3b")).toBe(true);
    expect(isValidModelID("model.v2")).toBe(true);
    expect(isValidModelID("m_1:x")).toBe(true);
    expect(isValidModelID("a")).toBe(true);
  });

  it("rejects URL-unsafe characters", () => {
    expect(isValidModelID("has space")).toBe(false);
    expect(isValidModelID("has/slash")).toBe(false);
    expect(isValidModelID("bad?name")).toBe(false);
  });

  it("rejects a non-alphanumeric first character", () => {
    expect(isValidModelID("-leading")).toBe(false);
    expect(isValidModelID(".leading")).toBe(false);
  });

  it("enforces the 128 character limit", () => {
    expect(isValidModelID("a".repeat(128))).toBe(true);
    expect(isValidModelID("a".repeat(129))).toBe(false);
    expect(isValidModelID("")).toBe(false);
  });
});
