import { describe, expect, it } from "vitest";

import { fmtMetric } from "./metrics";

describe("fmtMetric", () => {
  it("formats a finite number to the requested precision", () => {
    expect(fmtMetric(1.23456, 2)).toBe("1.23");
    expect(fmtMetric(42, 0)).toBe("42");
  });

  it("defaults to 2 digits when precision is omitted (the common call form)", () => {
    expect(fmtMetric(1.236)).toBe("1.24");
  });

  it("returns an em dash for null, undefined, or non-finite input", () => {
    expect(fmtMetric(null)).toBe("—");
    expect(fmtMetric(undefined)).toBe("—");
    expect(fmtMetric(Number.NaN)).toBe("—");
    expect(fmtMetric(Number.POSITIVE_INFINITY)).toBe("—");
  });
});
