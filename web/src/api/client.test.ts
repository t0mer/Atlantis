import { afterEach, expect, test, vi } from "vitest";
import { api, ApiError, setCsrfToken, setUnauthorizedHandler } from "./client";

afterEach(() => vi.restoreAllMocks());

function mockFetch(status: number, body: unknown) {
  return vi
    .spyOn(globalThis, "fetch")
    .mockImplementation(async () => new Response(body === undefined ? null : JSON.stringify(body), { status }));
}

test("sends the CSRF token on mutations only", async () => {
  setCsrfToken("tok");
  const f = mockFetch(200, { ok: true });
  await api("/api/v1/clients");
  await api("/api/v1/clients/x", { method: "PATCH", body: { watch: true } });
  const get = f.mock.calls[0]![1] as RequestInit;
  const patch = f.mock.calls[1]![1] as RequestInit;
  expect((get.headers as Record<string, string>)["X-CSRF-Token"]).toBeUndefined();
  expect((patch.headers as Record<string, string>)["X-CSRF-Token"]).toBe("tok");
});

test("401 outside auth calls the unauthorized handler", async () => {
  const handler = vi.fn();
  setUnauthorizedHandler(handler);
  mockFetch(401, { error: "login required" });
  await expect(api("/api/v1/nodes")).rejects.toBeInstanceOf(ApiError);
  expect(handler).toHaveBeenCalledOnce();
});

test("errors carry the server's message", async () => {
  mockFetch(501, { error: "not available yet" });
  await expect(api("/api/v1/nodes/m/reboot", { method: "POST" })).rejects.toThrow("not available yet");
});
