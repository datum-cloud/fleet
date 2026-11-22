import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import * as management from '../lib/inventory-management';
import { ApiError } from '../lib/api';

vi.mock('../lib/inventory-management', async () => ({ ...await vi.importActual('../lib/inventory-management'), useInventoryPermission: vi.fn(), createInventoryRecord: vi.fn() }));
const permission = vi.mocked(management.useInventoryPermission);
const create = vi.mocked(management.createInventoryRecord);
function renderManagement() { return render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind="Facility" facilities={[]} /></QueryClientProvider>); }
beforeEach(() => { vi.clearAllMocks(); permission.mockReturnValue({ data: true, isFetching: false, error: null } as never); });

describe('management affordances', () => {
  it('explains session expiry during the permission check without exposing raw errors', () => {
    permission.mockReturnValue({ data: undefined, isFetching: false, error: new ApiError(401, 'private upstream error') } as never);
    renderManagement();
    expect(screen.getByText(/Your session expired/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Create facility' })).not.toBeInTheDocument();
    expect(screen.queryByText(/private upstream error/)).not.toBeInTheDocument();
  });
  it('hides create from viewers, denied users and unresolved reviews', () => {
    permission.mockReturnValue({ data: false, isFetching: false, error: null } as never);
    renderManagement();
    expect(screen.queryByRole('button', { name: 'Create facility' })).not.toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();
  });
  it('permits an authorized manager to create and confirms only an acknowledged result', async () => {
    create.mockResolvedValue({ metadata: { name: 'example', uid: 'synthetic' } } as never);
    renderManagement();
    fireEvent.click(screen.getByText('Manage facilities'));
    fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
    fireEvent.change(screen.getByLabelText('Record name'), { target: { value: 'example' } });
    fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Example' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create facility record' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Facility record created.'));
  });
  it.each([401, 403, 500])('retains safe input without false success on submit failure %s', async status => {
    create.mockRejectedValue(new ApiError(status, 'Raw server error with private information'));
    renderManagement();
    fireEvent.click(screen.getByText('Manage facilities'));
    fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
    fireEvent.change(screen.getByLabelText('Record name'), { target: { value: 'example' } });
    fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Unsaved facility' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create facility record' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('input is kept here'));
    expect(screen.getByLabelText('Display name')).toHaveValue('Unsaved facility');
    expect(screen.getByRole('button', { name: 'Create facility record' })).toBeDisabled();
    expect(screen.queryByText(/Raw server error|record created/)).not.toBeInTheDocument();
  });
});
