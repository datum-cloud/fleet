import { createContext, useContext } from 'react';
import { CancelledError } from '@tanstack/react-query';
import { SessionHold } from './session-hold';

export class InventoryReadScope {
  private active = true;
  private generation = 0;
  private readonly pauses = new SessionHold();

  suspend() { this.active = false; this.generation++; this.pauses.notify(); }
  resume() { this.active = true; this.pauses.notify(); }

  /**
   * Pauses reads without cancelling them: a read waits to start, and a
   * finished read waits to deliver, until every reason is released. Loaded
   * data stays on screen. `suspend()` still cancels.
   */
  hold(reason: string) { this.pauses.hold(reason); }
  release(reason: string) { this.pauses.release(reason); }
  get held() { return this.pauses.held; }

  async read<Value>(fetcher: () => Promise<Value>): Promise<Value> {
    const generation = this.generation;
    const current = () => this.active && generation === this.generation;
    const assertCurrent = () => {
      if (!current()) throw new CancelledError({ revert: true });
    };
    // Only an actual hold adds an await, so unheld reads keep their timing.
    const unheld = async () => { while (this.pauses.held && current()) await this.pauses.changed(); assertCurrent(); };
    if (this.pauses.held) await unheld(); else assertCurrent();
    try {
      const value = await fetcher();
      if (this.pauses.held) await unheld(); else assertCurrent();
      return value;
    } catch (error) {
      if (this.pauses.held) await unheld(); else assertCurrent();
      throw error;
    }
  }
}

export const InventoryReadContext = createContext<InventoryReadScope | undefined>(undefined);

export function useInventoryRead<Value>(fetcher: () => Promise<Value>) {
  const scope = useContext(InventoryReadContext);
  return () => scope ? scope.read(fetcher) : fetcher();
}
