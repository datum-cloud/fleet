import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { expect } from 'vitest';

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

/**
 * Chooses a category and waits until React has rendered its directory.
 *
 * RouterProvider applies router state to React inside startTransition, so
 * `router.state.location` and `router.state.navigation` change before the
 * page commits, and the previous category's search box stays enabled until it
 * does. The new category's own search box, named for it, is the committed
 * signal; it does not depend on the switcher trigger, so callers can still
 * assert the trigger's label independently.
 */
export async function switchCategory(title: string, navigation = categoryNavigation()) {
  chooseCategory(title, navigation);
  await waitFor(() => expect(screen.getByRole('searchbox', { name: `Search ${title.toLowerCase()}` })).toBeEnabled());
}
