import { fireEvent, screen, within } from '@testing-library/react';

/** Opens a datum-ui Select from the keyboard and returns its listbox. */
export function openSelect(trigger: HTMLElement) {
  fireEvent.keyDown(trigger, { key: 'Enter' });
  return screen.getByRole('listbox');
}

/** Closes an open datum-ui Select without choosing. */
export function closeSelect() {
  const listbox = screen.queryByRole('listbox');
  if (listbox) fireEvent.keyDown(listbox, { key: 'Escape' });
}

/** The Select's options as a reader sees them on opening it; the Select is closed again. */
export function selectOptions(trigger: HTMLElement) {
  const options = within(openSelect(trigger)).queryAllByRole('option').map(option => ({
    value: option.dataset.value ?? '',
    text: option.textContent ?? '',
    disabled: option.getAttribute('aria-disabled') === 'true' || option.hasAttribute('data-disabled'),
    title: option.getAttribute('title') ?? undefined,
    selected: option.getAttribute('data-state') === 'checked',
    element: option,
  }));
  closeSelect();
  return options;
}

/** The chosen option's value, read from the open Select; '' when nothing or the clearing option is chosen. */
export function selectedValue(trigger: HTMLElement) {
  return selectOptions(trigger).find(option => option.selected)?.value ?? '';
}

/** Opens the Select and chooses the option with this value; '' chooses the option that clears the field. */
export function chooseValue(trigger: HTMLElement, value: string) {
  const option = openSelect(trigger).querySelector<HTMLElement>(`[role="option"][data-value="${CSS.escape(value)}"]`);
  if (!option) { closeSelect(); throw new Error(`No option with value ${JSON.stringify(value)}`); }
  fireEvent.click(option);
}

/** Opens the Select and chooses the option with this accessible name. */
export function chooseOption(trigger: HTMLElement, name: string | RegExp) {
  fireEvent.click(within(openSelect(trigger)).getByRole('option', { name }));
}

/** Sets a field the way a reader would: a datum-ui Select is opened and chosen from, any other control is typed into. */
export function setField(field: HTMLElement, value: string) {
  if (field.getAttribute('role') === 'combobox') chooseValue(field, value);
  else fireEvent.change(field, { target: { value } });
}

/** Opens the list toolbar's Filter popover, if it is closed, so its selects can be used. */
export function openFilters() {
  if (screen.queryByRole('combobox', { name: /^Filter by / })) return;
  fireEvent.click(screen.getByRole('button', { name: /^Filter\b/ }));
}
