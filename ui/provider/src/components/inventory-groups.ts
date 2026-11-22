export const inventoryGroups = [
  { title: 'Equipment', kinds: ['Device', 'Cable', 'PowerFeed'], description: 'Physical devices, recorded connections, and power supplies.' },
  { title: 'Locations', kinds: ['Site', 'Facility', 'Rack'], description: 'Operational sites group premises; facilities identify physical premises, and racks locate equipment within them.' },
  { title: 'Installation plans', kinds: ['DeviceClaim'], description: 'DeviceClaim intent, legacy slots, and registration history. Not every legacy claim is an installation plan; claims do not allocate capacity.' },
  { title: 'Hardware catalog', kinds: ['Manufacturer', 'DeviceType'], description: 'Reference manufacturers and model templates, not installed equipment or available capacity.' },
] as const;

export function inventoryGroup(kind: string) {
  return inventoryGroups.find(group => group.kinds.some(item => item === kind)) ?? inventoryGroups[0];
}
