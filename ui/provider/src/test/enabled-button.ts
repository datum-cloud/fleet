import { screen, waitFor } from '@testing-library/react';
import { expect } from 'vitest';

export async function findEnabledButton(options: Parameters<typeof screen.findByRole>[1]) {
  await waitFor(() => expect(screen.getByRole('button', options)).toBeEnabled());
  return screen.getByRole('button', options);
}
