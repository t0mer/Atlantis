import { QueryClient } from "@tanstack/react-query";
import { expect, test } from "vitest";
import { applyMessage } from "./live";
import type { DecoEvent } from "./types";

test("a live event is prepended once and the session replaced", () => {
  const qc = new QueryClient();
  const old: DecoEvent = { id: 1, ts: "", type: "client.online", subject: "a", payload: {} };
  qc.setQueryData(["events", ""], [old]);
  const e: DecoEvent = { id: 2, ts: "", type: "client.offline", subject: "b", payload: {} };
  applyMessage(qc, "event", e);
  applyMessage(qc, "event", e);
  expect(qc.getQueryData<DecoEvent[]>(["events", ""])!.map((x) => x.id)).toEqual([2, 1]);
  applyMessage(qc, "session", { state: "paused", since: "", logins: 1 });
  expect(qc.getQueryData<{ state: string }>(["session"])!.state).toBe("paused");
});

test("a clients message refreshes open client pages but not sparklines", () => {
  const qc = new QueryClient();
  qc.setQueryData(["client", "aa", "24h"], {});
  qc.setQueryData(["spark", "aa"], {});
  applyMessage(qc, "clients", []);
  expect(qc.getQueryState(["client", "aa", "24h"])!.isInvalidated).toBe(true);
  expect(qc.getQueryState(["spark", "aa"])!.isInvalidated).toBe(false);
});
