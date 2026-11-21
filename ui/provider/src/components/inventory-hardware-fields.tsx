import type { Device, DeviceClaim, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import { cableMedia, componentTypes, isTermination, type ReferencedEndpoint } from '../lib/inventory-hardware';
import type { InventoryFormValues } from '../lib/inventory-management';

export function InventoryHardwareFields({ kind, values, onChange, namespace, claims, devices, feeds, locations }: { kind: 'Device' | 'Cable' | 'DeviceClaim'; values: InventoryFormValues; onChange: (values: InventoryFormValues) => void; namespace: string; claims: DeviceClaim[]; devices: Device[]; feeds: PhysicalRecord[]; locations: PhysicalRecord[] }) {
  const field = (name: keyof InventoryFormValues, value: unknown) => onChange({ ...values, [name]: value });
  if (kind === 'DeviceClaim') return <fieldset><legend>New installation plan</legend>
    <label>Recorded location<select required value={values.planLocation?.uid ?? ''} onChange={event => { const location = locations.find(record => record.metadata.uid === event.target.value); field('planLocation', location ? { kind: location.kind, name: location.metadata.name, uid: location.metadata.uid!, resourceVersion: location.metadata.resourceVersion! } : undefined); }}><option value="">Choose a facility or rack</option>{locations.filter(record => record.metadata.namespace === namespace && record.metadata.uid && record.metadata.resourceVersion && !record.metadata.deletionTimestamp).map(record => <option key={record.metadata.uid} value={record.metadata.uid}>{record.kind} · {record.spec.displayName}</option>)}</select></label>
    <div className="inventory-form-grid">{([['manufacturer', 'Expected manufacturer'], ['model', 'Expected model'], ['serial', 'Expected serial (optional)']] as const).map(([name, label]) => <label key={name}>{label}<input required={name === 'model'} maxLength={253} value={values[name] ?? ''} onChange={event => field(name, event.target.value)} /></label>)}</div>
    <p className="inventory-muted">This records a new installation at a facility or rack with position unknown. It reserves no units and moves no existing equipment. Placement is immutable; corrections require a separately reviewed handover.</p>
  </fieldset>;
  if (kind === 'Device') return <>
    <fieldset><legend>Installation & identity</legend>
      <label>Installation plan<select required value={values.claim?.uid ?? ''} onChange={event => { const claim = claims.find(item => item.metadata.uid === event.target.value); field('claim', claim ? { name: claim.metadata.name, uid: claim.metadata.uid!, resourceVersion: claim.metadata.resourceVersion! } : undefined); }}><option value="">Choose an existing installation plan</option>{claims.filter(claim => claim.metadata.namespace === namespace && claim.metadata.uid && claim.metadata.resourceVersion && !claim.metadata.deletionTimestamp && claim.spec.lifecycle === 'Installation').map(claim => <option key={claim.metadata.uid} value={claim.metadata.uid}>{claim.spec.displayName || claim.metadata.name}</option>)}</select></label>
      <p className="inventory-muted">Identity and installation binding cannot be changed later. Existing registrations are kept; this does not replace hardware or identify the active device. Legacy slot bindings are not offered.</p>
      <div className="inventory-form-grid">{([['manufacturer', 'Manufacturer'], ['model', 'Model'], ['serial', 'Serial number'], ['systemUUID', 'System UUID'], ['assetTag', 'Asset tag']] as const).map(([name, label]) => <label key={name}>{label}<input required={name === 'model'} maxLength={253} value={values[name] ?? ''} onChange={event => field(name, event.target.value)} /></label>)}</div>
      <p className="inventory-muted">A real serial or system UUID is required. Leave unknown values blank; do not use an asset tag or provider ID as a serial.</p>
    </fieldset>
    <fieldset><legend>Physical components</legend><p className="inventory-muted">Record physical ports and parts only. Empty means not recorded, not absent. No logical interfaces or IP assignments.</p>
      {(values.components ?? []).map((component, index) => <div className="inventory-component-editor" key={index}>
        <div className="inventory-form-grid">{(['id', 'name', 'connector'] as const).map(name => <label key={name}>{`Component ${index + 1} ${name}`}<input required={name !== 'connector'} maxLength={name === 'id' ? 63 : 253} value={component[name] ?? ''} onChange={event => field('components', values.components!.map((item, position) => position === index ? { ...item, [name]: name === 'connector' ? event.target.value || undefined : event.target.value } : item))} /></label>)}
          <label>{`Component ${index + 1} type`}<select value={component.type} onChange={event => field('components', values.components!.map((item, position) => position === index ? { ...item, type: event.target.value as PhysicalComponent['type'] } : item))}>{componentTypes.map(type => <option key={type}>{type}</option>)}</select></label>
          <label>{`Component ${index + 1} positions`}<input required type="number" min={1} max={256} value={component.positions ?? ''} onChange={event => field('components', values.components!.map((item, position) => position === index ? { ...item, positions: Number(event.target.value) } : item))} /></label>
        </div><button type="button" className="inventory-readonly" onClick={() => field('components', values.components!.filter((_, position) => position !== index))}>{`Remove component ${index + 1}`}</button>
      </div>)}
      <button type="button" className="inventory-readonly" disabled={(values.components?.length ?? 0) >= 256} onClick={() => field('components', [...values.components ?? [], { id: '', name: '', type: 'NetworkPort', positions: 1 }])}>Add component</button>
    </fieldset>
  </>;
  const endpoints: { key: string; label: string; endpoint: ReferencedEndpoint }[] = [];
  for (const record of [...devices, ...feeds]) {
    if (record.metadata.namespace !== namespace || !record.metadata.uid || !record.metadata.resourceVersion || record.metadata.deletionTimestamp) continue;
    const device = 'identity' in record.spec;
    const components = device ? (record as Device).spec.components : (record as PhysicalRecord).spec.terminations;
    for (const component of components ?? []) {
      if (!isTermination(component) || !component.positions) continue;
      for (let position = 1; position <= Math.min(component.positions, 256); position++) {
        const endpoint: ReferencedEndpoint = { kind: device ? 'Device' : 'PowerFeed', name: record.metadata.name, uid: record.metadata.uid, resourceVersion: record.metadata.resourceVersion, componentID: component.id, position };
        endpoints.push({ key: JSON.stringify(endpoint), label: `${record.spec.displayName || record.metadata.name} / ${component.name} · ${component.type} · ${position}`, endpoint });
      }
    }
  }
  const lanes = values.cableLanes ?? [{ id: 'lane-1' }];
  return <>
    <label>Cable medium<select required value={values.medium ?? ''} onChange={event => field('medium', event.target.value)}><option value="">Choose medium</option>{cableMedia.map(medium => <option key={medium}>{medium}</option>)}</select></label>
    <fieldset><legend>Physical endpoints</legend><p className="inventory-muted">Only readable recorded ports with known positions are offered. Review existing cable connections before saving. Recording a cable does not reserve ports or verify a working link.</p>
      {lanes.map((lane, index) => <div className="inventory-component-editor" key={index}><label>{`Lane ${index + 1} ID`}<input required maxLength={63} value={lane.id} onChange={event => field('cableLanes', lanes.map((item, position) => position === index ? { ...item, id: event.target.value } : item))} /></label><div className="inventory-form-grid">{(['a', 'b'] as const).map(side => <label key={side}>{`Lane ${index + 1} end ${side.toUpperCase()}`}<select required value={lane[side] ? JSON.stringify(lane[side]) : ''} onChange={event => field('cableLanes', lanes.map((item, position) => position === index ? { ...item, [side]: endpoints.find(endpoint => endpoint.key === event.target.value)?.endpoint } : item))}><option value="">Choose a physical endpoint</option>{endpoints.map(endpoint => <option key={endpoint.key} value={endpoint.key}>{endpoint.label}</option>)}</select></label>)}</div>{lanes.length > 1 && <button type="button" className="inventory-readonly" onClick={() => field('cableLanes', lanes.filter((_, position) => position !== index))}>{`Remove lane ${index + 1}`}</button>}</div>)}
      <button type="button" className="inventory-readonly" disabled={lanes.length >= 64} onClick={() => field('cableLanes', [...lanes, { id: '' }])}>Add lane</button>
    </fieldset>
  </>;
}
