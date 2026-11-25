import { fireEvent, screen, within } from '@testing-library/react';

// The inventory category switcher: a trigger inside the "Inventory categories"
// navigation element that opens a datum-ui Popover of category links.
export function categoryNavigation() {
  return screen.getByRole('navigation', { name: 'Inventory categories' });
}

export function categoryTrigger(navigation = categoryNavigation()) {
  return within(navigation).getByRole('button', { name: /^Inventory category: / });
}

/** Opens the menu (if closed) and returns it. */
export function categoryMenu(navigation = categoryNavigation()) {
  const trigger = categoryTrigger(navigation);
  if (trigger.getAttribute('aria-expanded') !== 'true') fireEvent.click(trigger);
  return screen.getByRole('dialog', { name: 'Inventory categories' });
}

/** Opens the menu (if closed) and returns the link for a category title. */
export function categoryLink(title: string, navigation = categoryNavigation()) {
  return within(categoryMenu(navigation)).getByRole('link', { name: title });
}

/** Chooses a category from the switcher. */
export function chooseCategory(title: string, navigation = categoryNavigation()) {
  fireEvent.click(categoryLink(title, navigation));
}
