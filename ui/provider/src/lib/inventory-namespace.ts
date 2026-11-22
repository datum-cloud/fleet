export const INVENTORY_NAMESPACE = 'default';

export function inInventoryNamespace(record: { metadata?: { namespace?: string } }): boolean {
  return record.metadata?.namespace === INVENTORY_NAMESPACE;
}
