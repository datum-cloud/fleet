/**
 * Named pauses on a session scope. While any reason is held, reads and
 * operations wait instead of failing; releasing the last reason lets them go
 * on. Waiters also wake on any other change to the scope (invalidation, a new
 * lifecycle) so they can re-check whether they are still current.
 */
export class SessionHold {
  private reasons = new Set<string>();
  private waiters: (() => void)[] = [];

  get held() { return this.reasons.size > 0; }

  hold(reason: string) { this.reasons.add(reason); }

  release(reason: string) { if (this.reasons.delete(reason) && !this.held) this.notify(); }

  /** Wakes every waiter so it re-checks its own state. */
  notify() {
    const waiters = this.waiters;
    this.waiters = [];
    for (const wake of waiters) wake();
  }

  /** Resolves on the next hold release or notify. */
  changed() { return new Promise<void>(resolve => { this.waiters.push(resolve); }); }
}
