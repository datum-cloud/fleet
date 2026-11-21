import { createContext, useContext } from 'react';
import { CancelledError } from '@tanstack/react-query';

export class InventoryReadScope {
  private active = true;
  private generation = 0;

  suspend() { this.active = false; this.generation++; }
  resume() { this.active = true; }

  async read<Value>(fetcher: () => Promise<Value>): Promise<Value> {
    const generation = this.generation;
    const assertCurrent = () => {
      if (!this.active || generation !== this.generation) throw new CancelledError({ revert: true });
    };
    assertCurrent();
    try {
      const value = await fetcher();
      assertCurrent();
      return value;
    } catch (error) {
      assertCurrent();
      throw error;
    }
  }
}

export const InventoryReadContext = createContext<InventoryReadScope | undefined>(undefined);

export function useInventoryRead<Value>(fetcher: () => Promise<Value>) {
  const scope = useContext(InventoryReadContext);
  return () => scope ? scope.read(fetcher) : fetcher();
}
