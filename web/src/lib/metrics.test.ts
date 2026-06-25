import { describe, expect, it } from "vitest";

import { REFERENCE_ALGO, fmtMetric, fmtRelativePoa, relativePoa } from "./metrics";

describe("REFERENCE_ALGO", () => {
  it("is the system-optimal router (PoA ≈ 1 anchor)", () => {
    expect(REFERENCE_ALGO).toBe("systemoptimal");
  });
});

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

describe("relativePoa", () => {
  it("divides an algorithm's PoA by the reference PoA", () => {
    expect(relativePoa(1.5, 1)).toBe(1.5);
    expect(relativePoa(1.0, 1.0)).toBe(1);
  });

  it("returns null when either side is missing or the reference is zero", () => {
    expect(relativePoa(null, 1)).toBeNull();
    expect(relativePoa(1.5, null)).toBeNull();
    expect(relativePoa(1.5, 0)).toBeNull();
    expect(relativePoa(Number.NaN, 1)).toBeNull();
  });

  it("returns null when the reference itself is non-finite", () => {
    expect(relativePoa(1.5, Number.NaN)).toBeNull();
    expect(relativePoa(1.5, Number.POSITIVE_INFINITY)).toBeNull();
  });
});

describe("fmtRelativePoa", () => {
  it("renders a ratio with a multiplication sign", () => {
    expect(fmtRelativePoa(1.273)).toBe("1.27×");
    expect(fmtRelativePoa(1)).toBe("1.00×");
  });

  it("renders an em dash when not computable", () => {
    expect(fmtRelativePoa(null)).toBe("—");
  });
});
