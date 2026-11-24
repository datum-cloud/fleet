import { screen } from '@testing-library/react';
import { chooseValue, selectOptions } from './field-select';

/** Picks a cable end through the device-then-port picker: the device or feed whose name, or whose port's name, contains the text. */
export function chooseEndpoint(label: string, text: string) {
  for (const option of selectOptions(screen.getByLabelText(`${label} device`)).filter(item => item.value)) {
    chooseValue(screen.getByLabelText(`${label} device`), option.value);
    const port = screen.getByLabelText(label);
    const match = selectOptions(port).filter(item => item.value).find(item => option.text.includes(text) || item.text.includes(text));
    if (match) { chooseValue(port, match.value); return; }
  }
  throw new Error(`No cable endpoint matches ${text}`);
}
