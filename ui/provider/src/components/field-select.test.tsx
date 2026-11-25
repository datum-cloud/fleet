import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Label } from '@datum-cloud/datum-ui/label';
import { useState } from 'react';
import { MemoryRouter } from 'react-router';
import { expect, it, vi } from 'vitest';
import { chooseValue, openSelect, selectedValue, selectOptions, openFilters } from '../test/field-select';
import { FieldSelect } from './field-select';
import { InventoryForm } from './inventory-form-surface';
import { InventoryToolbar } from './inventory-directory-heading';
import { sections } from './inventory-navigation';

const devices = sections.find(section => section.kind === 'Device')!;

it('filters the directory from the keyboard and clears with the All choice', () => {
  const onLocation = vi.fn();
  const onModel = vi.fn();
  render(<MemoryRouter><InventoryToolbar section={devices} location="default/site-a" onLocation={onLocation} facilities={[{ value: 'default/site-a', label: 'Site A' }, { value: 'default/site-b', label: 'Site B' }]} onModel={onModel} models={['Example 1U']} /></MemoryRouter>);
  openFilters();
  const facility = screen.getByRole('combobox', { name: 'Filter by facility' });
  expect(facility).toHaveTextContent('Site A');
  expect(selectOptions(facility).map(option => option.text)).toEqual(['All facilities', 'Site A', 'Site B']);
  chooseValue(facility, 'default/site-b');
  expect(onLocation).toHaveBeenLastCalledWith('default/site-b');
  chooseValue(facility, '');
  expect(onLocation).toHaveBeenLastCalledWith('');
  const model = screen.getByRole('combobox', { name: 'Filter by model' });
  expect(model).toHaveTextContent('All models');
  expect(selectedValue(model)).toBe('');
  chooseValue(model, 'Example 1U');
  expect(onModel).toHaveBeenLastCalledWith('Example 1U');
});

it('disables a filter that has no handler', () => {
  render(<MemoryRouter><InventoryToolbar section={devices} facilities={[{ value: 'default/site-a', label: 'Site A' }]} /></MemoryRouter>);
  // With no filter handlers the Filter button itself is disabled.
  expect(screen.getByRole('button', { name: 'Filter' })).toBeDisabled();
});

function RequiredPlan({ onSubmit }: { onSubmit: () => void }) {
  const [plan, setPlan] = useState('');
  return <InventoryForm onSubmit={onSubmit} message="">
    <Label>Installation plan<FieldSelect required aria-describedby="plan-hint" value={plan} onValueChange={setPlan} placeholder="Choose an installation plan" options={[{ value: 'plan-uid', label: 'Plan A' }, { value: 'taken-uid', label: 'Plan B · has device', attention: true, hint: 'A device is already registered to this plan' }]} /></Label>
    <small id="plan-hint">Can't be changed later.</small>
    <button type="submit">Save</button>
  </InventoryForm>;
}

it('reports an empty required Select against its trigger and clears the report once chosen', () => {
  const onSubmit = vi.fn();
  render(<RequiredPlan onSubmit={onSubmit} />);
  const plan = screen.getByRole('combobox', { name: 'Installation plan' });
  expect(plan).toHaveAccessibleDescription("Can't be changed later.");
  expect(plan).toHaveTextContent('Choose an installation plan');
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  expect(onSubmit).not.toHaveBeenCalled();
  const report = screen.getByRole('button', { name: /^Installation plan: / });
  expect(plan).toHaveAttribute('aria-invalid', 'true');
  expect(plan).toHaveAccessibleDescription(report.textContent!);
  fireEvent.click(report);
  expect(plan).toHaveFocus();
  chooseValue(plan, 'plan-uid');
  expect(plan).not.toHaveAttribute('aria-invalid');
  expect(screen.queryByRole('button', { name: /^Installation plan: / })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  expect(onSubmit).toHaveBeenCalledTimes(1);
});

it('keeps option hints, amber marking and the placeholder out of the choices', () => {
  render(<RequiredPlan onSubmit={vi.fn()} />);
  const options = selectOptions(screen.getByRole('combobox', { name: 'Installation plan' }));
  expect(options.map(option => option.value)).toEqual(['plan-uid', 'taken-uid']);
  expect(options[1].element).toHaveClass('inventory-option-attention');
  expect(options[1].title).toBe('A device is already registered to this plan');
});

it('moves through options with the arrow keys and chooses with Enter', async () => {
  const onValueChange = vi.fn();
  render(<FieldSelect aria-label="Face" value="" onValueChange={onValueChange} none="Unknown" options={[{ value: 'front', label: 'Front' }, { value: 'rear', label: 'Rear' }]} />);
  openSelect(screen.getByRole('combobox', { name: 'Face' }));
  await waitFor(() => expect(document.activeElement).toHaveTextContent('Unknown'));
  fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' });
  await waitFor(() => expect(document.activeElement).toHaveTextContent('Front'));
  fireEvent.keyDown(document.activeElement!, { key: 'Enter' });
  expect(onValueChange).toHaveBeenCalledWith('front');
});
