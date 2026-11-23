import { fireEvent, screen } from '@testing-library/react';

/** Picks a cable end through the device-then-port picker: the device or feed whose name, or whose port's name, contains the text. */
export function chooseEndpoint(label: string, text: string) {
  const source = screen.getByLabelText(`${label} device`) as HTMLSelectElement;
  for (const option of [...source.options].filter(item => item.value)) {
    fireEvent.change(source, { target: { value: option.value } });
    const port = screen.getByLabelText(label) as HTMLSelectElement;
    const match = [...port.options].filter(item => item.value).find(item => option.text.includes(text) || item.text.includes(text));
    if (match) { fireEvent.change(port, { target: { value: match.value } }); return; }
  }
  throw new Error(`No cable endpoint matches ${text}`);
}
