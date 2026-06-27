// Tests for the trailing-edge debouncer (#104): a burst of calls (a slider drag)
// collapses to ONE call on settle, the call carries the LAST args (the released value),
// and cancel() drops a pending call. Fake timers make the settle window deterministic.

import { afterEach, describe, expect, it, vi } from "vitest";

import { debounce } from "./debounce";

afterEach(() => {
  vi.useRealTimers();
});

describe("debounce", () => {
  it("fires exactly once after the settle window, with the last args", () => {
    vi.useFakeTimers();
    const fn = vi.fn();
    const d = debounce(fn, 300);

    // A drag: many rapid calls within the window.
    d(1);
    d(2);
    d(3);
    expect(fn).not.toHaveBeenCalled(); // nothing mid-drag

    vi.advanceTimersByTime(299);
    expect(fn).not.toHaveBeenCalled(); // still settling

    vi.advanceTimersByTime(1);
    expect(fn).toHaveBeenCalledTimes(1); // one call on settle
    expect(fn).toHaveBeenCalledWith(3); // the released value
  });

  it("fires again for a fresh burst after the first settled", () => {
    vi.useFakeTimers();
    const fn = vi.fn();
    const d = debounce(fn, 100);

    d("a");
    vi.advanceTimersByTime(100);
    d("b");
    vi.advanceTimersByTime(100);

    expect(fn).toHaveBeenCalledTimes(2);
    expect(fn).toHaveBeenNthCalledWith(1, "a");
    expect(fn).toHaveBeenNthCalledWith(2, "b");
  });

  it("cancel() drops a pending trailing call", () => {
    vi.useFakeTimers();
    const fn = vi.fn();
    const d = debounce(fn, 200);

    d("x");
    d.cancel();
    vi.advanceTimersByTime(500);
    expect(fn).not.toHaveBeenCalled();
  });
});
