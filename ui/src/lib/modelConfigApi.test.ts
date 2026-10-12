import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchListeners } from "./modelConfigApi";

// The shared requestJSON helper surfaces server errors. llama-swap emits the
// OpenAI-compatible envelope {src, error: {message, ...}}; a bare string or an
// unparseable body must still degrade to something readable rather than
// "[object Object]".
describe("modelConfigApi error surfacing", () => {
  afterEach(() => vi.unstubAllGlobals());

  function stubError(status: number, body: unknown): void {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: false,
        status,
        json: async () => body,
      })),
    );
  }

  it("uses the nested error message", async () => {
    stubError(409, {
      src: "llama-swap",
      error: { message: "listen address already in use", type: "invalid_request_error", param: null, code: "" },
    });
    await expect(fetchListeners()).rejects.toThrow("listen address already in use");
  });

  it("accepts a bare string error", async () => {
    stubError(400, { error: "body must be JSON" });
    await expect(fetchListeners()).rejects.toThrow("body must be JSON");
  });

  it("keeps the status code when the body is unparseable", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: false,
        status: 502,
        json: async () => {
          throw new Error("not json");
        },
      })),
    );
    await expect(fetchListeners()).rejects.toThrow("502");
  });
});
