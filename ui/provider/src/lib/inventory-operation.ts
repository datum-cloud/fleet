import { createContext } from 'react';

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

  invalidate() { this.active = false; }

  startLifecycle() { this.generation++; this.active = true; }

  begin(parent?: InventoryOperationScope) {
    const generation = this.generation;
    const parentGeneration = parent?.generation;
    return new InventoryOperation(() => this.active && this.generation === generation && (!parent || (parent.active && parent.generation === parentGeneration)));
  }
}

export const InventoryOperationContext = createContext<InventoryOperationScope | undefined>(undefined);
export const InventoryOperationGenerationContext = createContext(0);
export const InventoryOperationReadyContext = createContext(true);

export class InventoryOperation {
  private dispatched = false;

  constructor(private readonly active: () => boolean) {}

  assertCurrent() {
    if (!this.active()) throw new InventoryOperationInvalidated(this.dispatched);
  }

  dispatch() {
    this.assertCurrent();
    this.dispatched = true;
  }

  /** Whether the write was handed to the network, so its outcome may be unknown. */
  get sent() { return this.dispatched; }
}
