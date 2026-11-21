import { CancelledError } from '@tanstack/react-query';
import { expect, it, vi } from 'vitest';
import { InventoryReadScope } from './inventory-read';

it('does not dispatch reads while identity is unverified', async () => {
  const scope = new InventoryReadScope();
  const fetcher = vi.fn(async () => 'private');
  scope.suspend();
  await expect(scope.read(fetcher)).rejects.toBeInstanceOf(CancelledError);
  expect(fetcher).not.toHaveBeenCalled();
  scope.resume();
  await expect(scope.read(fetcher)).resolves.toBe('private');
});

it.each([false, true])('discards late responses after suspension and renewal, failure=%s', async failure => {
  const scope = new InventoryReadScope();
  let finish!: () => void;
  const response = new Promise<string>((resolve, reject) => {
    finish = () => failure ? reject(new Error('old denial')) : resolve('old private data');
  });
  const result = scope.read(() => response);
  const assertion = expect(result).rejects.toBeInstanceOf(CancelledError);
  scope.suspend();
  scope.resume();
  finish();
  await assertion;
  await expect(scope.read(async () => 'fresh')).resolves.toBe('fresh');
});
