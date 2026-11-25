import { expect, test } from "vitest";
import { hoursOf } from "./SettingsPanels";

test("Go durations to hours", () => {
  expect(hoursOf("168h0m0s")).toBe(168);
  expect(hoursOf("2160h0m0s")).toBe(2160);
  expect(hoursOf("1h30m0s")).toBe(2);
  expect(hoursOf("45m0s")).toBe(1);
});
