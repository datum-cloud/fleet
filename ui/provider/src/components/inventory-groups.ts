// Inventory categories, grouped in the category switcher. Manufacturers and
// Device types are reference data, not installed equipment, so they live on
// the separate Catalog page.
export const inventoryGroups = [
  { title: 'Equipment', short: 'Equipment', kinds: ['Device', 'Cable', 'PowerFeed'] },
  { title: 'Locations', short: 'Locations', kinds: ['Site', 'Facility', 'Rack'] },
  { title: 'Installation plans', short: 'Plans', kinds: ['DeviceClaim'] },
] as const;

export const CATALOG_KINDS = ['Manufacturer', 'DeviceType'] as const;
export const catalogGroup = { title: 'Hardware catalog', short: 'Catalog', kinds: CATALOG_KINDS } as const;

export function isCatalogKind(kind: string | null | undefined): boolean {
  return CATALOG_KINDS.some(item => item === kind);
}

export function inventoryGroup(kind: string) {
  if (isCatalogKind(kind)) return catalogGroup;
  return inventoryGroups.find(group => group.kinds.some(item => item === kind)) ?? inventoryGroups[0];
}
