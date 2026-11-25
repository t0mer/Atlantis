import { expect, test } from "vitest";
import { ago, bandLabel, eventText, goDuration, rate, sessionSentence } from "./format";

test("rate", () => {
  expect(rate(0)).toBe("0");
  expect(rate(950)).toBe("950 b/s");
  expect(rate(4_200_000)).toBe("4.2 Mb/s");
  expect(rate(185_000_000)).toBe("185 Mb/s");
});

test("ago treats the zero time as never", () => {
  const now = Date.parse("2026-01-01T12:00:00Z");
  expect(ago("0001-01-01T00:00:00Z", now)).toBe("never");
  expect(ago("2026-01-01T11:59:50Z", now)).toBe("10s ago");
  expect(ago("2026-01-01T09:00:00Z", now)).toBe("3 h ago");
});

test("bands", () => {
  expect(bandLabel("band5")).toBe("5 GHz");
  expect(bandLabel("band2+band5")).toBe("Multi-link");
  expect(bandLabel("band5_1", true)).toBe("Wired");
});

test("event sentences use display names", () => {
  const e = { id: 1, ts: "", type: "client.offline" as const, subject: "aa", payload: {} };
  expect(eventText(e, () => "Noa's phone")).toBe("\u2068Noa's phone\u2069 went offline");
  // A Hebrew name is isolated, so it can't flip the English sentence.
  expect(eventText(e, () => "הטלפון של נועה")).toBe("\u2068הטלפון של נועה\u2069 went offline");
  expect(sessionSentence({ state: "auth_failed", since: "", logins: 0 }).tone).toBe("bad");
});

test("goDuration reads Go durations", () => {
  expect(goDuration("168h0m0s")).toBe("7 days");
  expect(goDuration("24h0m0s")).toBe("1 day");
  expect(goDuration("5m0s")).toBe("5 minutes");
  expect(goDuration("1h30m0s")).toBe("90 minutes");
  expect(goDuration("10s")).toBe("10 seconds");
  expect(goDuration("0s")).toBe("0 seconds");
  expect(goDuration("odd")).toBe("odd");
});
