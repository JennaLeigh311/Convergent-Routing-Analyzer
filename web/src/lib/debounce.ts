// A tiny trailing-edge debouncer (issue #104). The parameter controls fire on EVERY
// drag tick of a slider; this collapses that burst into a single call once the value
// has SETTLED (no change for `delayMs`), so a benchmark job is fired on release, never
// mid-drag. Only the last call's args survive — the settled value is what runs.
//
// It is framework-agnostic and pure (timer-only), so the debounce timing is unit-
// testable with fake timers, and the React layer just wires it to onChange.

/** A debounced function plus a `cancel()` to drop a pending trailing call. */
export interface Debounced<A extends unknown[]> {
  (...args: A): void;
  /** Cancel a pending invocation (e.g. on unmount) so it never fires late. */
  cancel: () => void;
}

/**
 * Wrap `fn` so rapid calls collapse to ONE trailing call `delayMs` after the last one.
 * Each call resets the timer; `cancel()` clears any pending call without firing it.
 */
export function debounce<A extends unknown[]>(
  fn: (...args: A) => void,
  delayMs: number,
): Debounced<A> {
  let timer: ReturnType<typeof setTimeout> | null = null;

  const debounced = (...args: A): void => {
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      fn(...args);
    }, delayMs);
  };

  debounced.cancel = (): void => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };

  return debounced;
}
