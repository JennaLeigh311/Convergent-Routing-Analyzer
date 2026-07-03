// Tests for the time-of-day / date <-> RFC3339 conversions (#113): combining a date and
// minutes-of-day into the /stream `start` instant round-trips through splitSimStart, and
// the formatters produce the human labels the UI shows. Pure string surgery, so no fake
// clock or timezone setup is needed — the assertions are exact.

import { describe, expect, it } from "vitest";

import {
  combineSimStart,
  formatInstant,
  formatMinutes,
  splitSimStart,
} from "./simStart";

describe("combineSimStart", () => {
  it("builds an RFC3339 UTC instant from a date and minutes-of-day", () => {
    expect(combineSimStart("2026-06-22", 8 * 60)).toBe("2026-06-22T08:00:00Z");
    expect(combineSimStart("2026-06-22", 0)).toBe("2026-06-22T00:00:00Z");
    expect(combineSimStart("2026-06-22", 17 * 60 + 30)).toBe("2026-06-22T17:30:00Z");
  });

  it("wraps out-of-range minutes into a valid time-of-day", () => {
    expect(combineSimStart("2026-06-22", 1440)).toBe("2026-06-22T00:00:00Z");
    expect(combineSimStart("2026-06-22", -30)).toBe("2026-06-22T23:30:00Z");
  });
});

describe("splitSimStart", () => {
  it("splits an instant back into date + minutes-of-day", () => {
    expect(splitSimStart("2026-06-22T08:00:00Z")).toEqual({
      date: "2026-06-22",
      minutes: 8 * 60,
    });
    expect(splitSimStart("2026-06-22T17:30:00Z")).toEqual({
      date: "2026-06-22",
      minutes: 17 * 60 + 30,
    });
  });

  it("round-trips with combineSimStart", () => {
    const rfc = "2026-01-05T13:45:00Z";
    const { date, minutes } = splitSimStart(rfc);
    expect(combineSimStart(date, minutes)).toBe(rfc);
  });

  it("degrades gracefully on an unparseable string", () => {
    expect(splitSimStart("nonsense")).toEqual({ date: "", minutes: 0 });
  });
});

describe("formatMinutes", () => {
  it("renders minutes-of-day as a zero-padded 24h clock", () => {
    expect(formatMinutes(0)).toBe("00:00");
    expect(formatMinutes(8 * 60)).toBe("08:00");
    expect(formatMinutes(17 * 60 + 5)).toBe("17:05");
  });

  it("wraps out-of-range minutes into a valid time-of-day", () => {
    expect(formatMinutes(1440)).toBe("00:00");
    expect(formatMinutes(-30)).toBe("23:30");
  });
});

describe("formatInstant", () => {
  it("renders a readable UTC label", () => {
    expect(formatInstant("2026-06-22T08:00:00Z")).toBe("Jun 22, 2026 · 08:00 UTC");
    expect(formatInstant("2026-01-05T13:45:00Z")).toBe("Jan 5, 2026 · 13:45 UTC");
  });

  it("returns an unparseable string verbatim", () => {
    expect(formatInstant("")).toBe("");
    expect(formatInstant("not-a-time")).toBe("not-a-time");
  });
});
