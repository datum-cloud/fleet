import { createContext } from 'react';
import { SessionHold } from './session-hold';

export class InventoryOperationInvalidated extends Error {
  constructor(readonly dispatched: boolean) {
    super(dispatched
      ? 'The save outcome is unknown because the session changed after dispatch. Check inventory before another attempt.'
      : 'The session or page changed before saving. No write was dispatched. Your input is kept here; return to inventory and reopen the form after checking your session.');
  }
}

export class InventoryOperationScope {
  private active = true;
  private generation = 0;
  private readonly pauses = new SessionHold();

  invalidate() { this.active = false; this.pauses.notify(); }

  startLifecycle() { this.generation++; this.active = true; this.pauses.notify(); }

  /**
   * Pauses operations of this scope without retiring them: `settle()` waits
   * until every reason is released. Used while the host checks who is signed
   * in; nothing is shown for it.
   */
  hold(reason: string) { this.pauses.hold(reason); }

  release(reason: string) { this.pauses.release(reason); }

  get held() { return this.pauses.held; }

  changed() { return this.pauses.changed(); }

  begin(parent?: InventoryOperationScope) {
    const generation = this.generation;
    const parentGeneration = parent?.generation;
    const current = () => this.active && this.generation === generation && (!parent || (parent.active && parent.generation === parentGeneration));
    const held = () => this.held || !!parent?.held;
    const changed = () => Promise.race([this.changed(), ...(parent ? [parent.changed()] : [])]);
    return new InventoryOperation(current, async () => { while (held() && current()) await changed(); });
  }
}

export const InventoryOperationContext = createContext<InventoryOperationScope | undefined>(undefined);
export const InventoryOperationGenerationContext = createContext(0);
export const InventoryOperationReadyContext = createContext(true);

export class InventoryOperation {
  private dispatched = false;

  constructor(private readonly active: () => boolean, private readonly unheld: () => Promise<void> = async () => {}) {}

  assertCurrent() {
    if (!this.active()) throw new InventoryOperationInvalidated(this.dispatched);
  }

  /**
   * Waits while the session is held for a check of who is signed in, then
   * asserts the operation is still current. Call it immediately before
   * `dispatch()`, so no write leaves while the signed-in user is unconfirmed.
   */
  async settle() {
    await this.unheld();
    this.assertCurrent();
  }

  dispatch() {
    this.assertCurrent();
    this.dispatched = true;
  }
}
