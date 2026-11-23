// `short` is the label at phone width, where the four full labels do not fit one row.
export const inventoryGroups = [
  { title: 'Equipment', short: 'Equipment', kinds: ['Device', 'Cable', 'PowerFeed'] },
  { title: 'Locations', short: 'Locations', kinds: ['Site', 'Facility', 'Rack'] },
  { title: 'Installation plans', short: 'Plans', kinds: ['DeviceClaim'] },
  { title: 'Hardware catalog', short: 'Catalog', kinds: ['Manufacturer', 'DeviceType'] },
] as const;

export function inventoryGroup(kind: string) {
  return inventoryGroups.find(group => group.kinds.some(item => item === kind)) ?? inventoryGroups[0];
}
