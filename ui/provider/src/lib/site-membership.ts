import type { PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';

export function facilityIdentifiers(facility: PhysicalRecord): string {
  return facility.spec.identifiers?.map(identifier => `${identifier.provider} / ${identifier.value} (${identifier.type})`).join(' · ') || 'Provider premises identifiers not recorded';
}

export function facilitySites(facility: PhysicalRecord, inventory: Inventory): string {
  if (inventory.issues?.Site) return 'Operational Site details unavailable';
  const sites = inventory.physical.filter(record => record.kind === 'Site' && !!record.metadata.uid && record.metadata.namespace === facility.metadata.namespace && !record.metadata.deletionTimestamp && record.spec.facilityRefs?.some(reference => reference.name === facility.metadata.name && !facility.metadata.deletionTimestamp));
  if (sites.length) return sites.map(site => `${site.spec.code} · ${site.spec.displayName}`).join(' / ');
  const unavailable = inventory.physical.some(record => record.kind === 'Site' && record.metadata.namespace === facility.metadata.namespace && record.spec.facilityRefs?.some(reference => reference.name === facility.metadata.name));
  return unavailable ? 'Operational Site membership unavailable' : 'Unmapped · no operational Site recorded';
}

export function siteFacilities(site: PhysicalRecord, inventory: Inventory): { facility?: PhysicalRecord; state: string }[] {
  if (inventory.issues?.Facility) return [{ state: 'Facility details unavailable' }];
  if (!site.metadata.uid || site.metadata.deletionTimestamp) return [{ state: 'Operational Site incarnation unavailable' }];
  return (site.spec.facilityRefs ?? []).map(reference => {
    const facility = inventory.physical.find(record => record.kind === 'Facility' && record.metadata.namespace === site.metadata.namespace && record.metadata.name === reference.name && !record.metadata.deletionTimestamp);
    return facility ? { facility, state: 'Recorded membership only' } : { state: 'Facility membership unresolved · missing or deleting record' };
  });
}
