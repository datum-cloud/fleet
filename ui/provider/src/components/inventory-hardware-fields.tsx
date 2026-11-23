import { Input } from '@datum-cloud/datum-ui/input';
import { Label } from '@datum-cloud/datum-ui/label';
import { Button } from '@datum-cloud/datum-ui/button';
import { useId, type ReactNode } from 'react';
import { catalogLabel } from '../lib/manufacturer-inventory';
import type { Device, DeviceClaim, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import { cabledEndpoints, cableMedia, componentTypes, deviceIdentityProblems, endpointKey, isTermination, systemUUIDPattern, type ReferencedEndpoint } from '../lib/inventory-hardware';
import type { InventoryFormValues } from '../lib/inventory-management';

type Lane = NonNullable<InventoryFormValues['cableLanes']>[number];
interface EndpointSource { key: string; label: string; group: string; ports: { key: string; label: string; cable?: string; endpoint: ReferencedEndpoint }[] }

/** A short line under the field it protects, linked as its description. */
function Hint({ id, children }: { id: string; children: ReactNode }) {
  return <small id={id} className="inventory-field-hint">{children}</small>;
}


export function InventoryHardwareFields({ kind, values, onChange, namespace, claims, devices, feeds, locations, catalog = [], cables = [], names }: { kind: 'Device' | 'Cable' | 'DeviceClaim'; values: InventoryFormValues; onChange: (values: InventoryFormValues) => void; namespace: string; claims: DeviceClaim[]; devices: Device[]; feeds: PhysicalRecord[]; locations: PhysicalRecord[]; catalog?: PhysicalRecord[]; cables?: PhysicalRecord[]; names?: ReactNode }) {
  const id = useId();
  const field = (name: keyof InventoryFormValues, value: unknown) => onChange({ ...values, [name]: value });
  const current = (record: { metadata: PhysicalRecord['metadata'] }) => record.metadata.namespace === namespace && !!record.metadata.uid && !!record.metadata.resourceVersion && !record.metadata.deletionTimestamp;
  const readable = catalog.filter(current);
  const models = readable.filter(record => record.kind === 'DeviceType').flatMap(model => {
    const manufacturer = readable.find(record => record.kind === 'Manufacturer' && record.metadata.name === model.spec.manufacturerRef?.name);
    return manufacturer ? [{ model, manufacturer }] : [];
  });
  const places = locations.filter(current);
  if (kind === 'DeviceClaim') return <>
    <div className="inventory-field">
      <Label>Location<select required aria-describedby={`${id}-location`} value={values.planLocation?.uid ?? ''} onChange={event => { const location = places.find(record => record.metadata.uid === event.target.value); field('planLocation', location ? { kind: location.kind, name: location.metadata.name, uid: location.metadata.uid!, resourceVersion: location.metadata.resourceVersion! } : undefined); }}><option value="">Choose a facility or rack</option>
        {([['Facility', 'Facilities'], ['Rack', 'Racks']] as const).map(([group, label]) => places.some(record => record.kind === group) && <optgroup key={group} label={label}>{places.filter(record => record.kind === group).map(record => <option key={record.metadata.uid} value={record.metadata.uid}>{record.spec.displayName}</option>)}</optgroup>)}
      </select></Label>
      <Hint id={`${id}-location`}>Location can't be changed later.</Hint>
    </div>
    <div className="inventory-form-grid">{([['manufacturer', 'Expected manufacturer'], ['model', 'Expected model'], ['serial', 'Expected serial']] as const).map(([name, label]) => <Label key={name}>{label}<Input required={name === 'model'} maxLength={253} value={values[name] ?? ''} onChange={event => field(name, event.target.value)} /></Label>)}</div>
  </>;
  if (kind === 'Device') {
    const problems = deviceIdentityProblems(values);
    const components = values.components ?? [];
    const plans = claims.filter(claim => current(claim) && claim.spec.lifecycle === 'Installation');
    const registered = new Set(devices.map(device => device.spec.claimRef?.name));
    const component = (index: number, change: Partial<PhysicalComponent>) => field('components', components.map((item, position) => position === index ? { ...item, ...change } : item));
    return <>
      <fieldset><legend>Installation & identity</legend>
        <div className="inventory-field">
          <Label>Installation plan<select required aria-describedby={`${id}-plan`} value={values.claim?.uid ?? ''} onChange={event => { const claim = plans.find(item => item.metadata.uid === event.target.value); field('claim', claim ? { name: claim.metadata.name, uid: claim.metadata.uid!, resourceVersion: claim.metadata.resourceVersion! } : undefined); }}><option value="">Choose an installation plan</option>
            {plans.map(claim => {
              // A plan with a device keeps it; marking it here stops a second registration by mistake.
              // Device creation needs the Device collection, so an unmarked plan is known to have none.
              const taken = registered.has(claim.metadata.name);
              return <option key={claim.metadata.uid} value={claim.metadata.uid} className={taken ? 'inventory-option-attention' : undefined} title={taken ? 'A device is already registered to this plan' : undefined}>{claim.spec.displayName || claim.metadata.name}{taken && ' · has device'}</option>;
            })}
          </select></Label>
          <Hint id={`${id}-plan`}>Can't be changed later.</Hint>
        </div>
        {names}
        <div className="inventory-field">
          <Label>Catalog model<select aria-describedby={`${id}-catalog`} value={models.some(({ model }) => model.metadata.uid === values.catalog?.uid) ? values.catalog?.uid ?? '' : values.catalog ? 'unavailable' : ''} onChange={event => {
            const choice = models.find(({ model }) => model.metadata.uid === event.target.value);
            field('catalog', choice ? { namespace, name: choice.model.metadata.name, uid: choice.model.metadata.uid!, resourceVersion: choice.model.metadata.resourceVersion!, manufacturer: { name: choice.manufacturer.metadata.name, uid: choice.manufacturer.metadata.uid!, resourceVersion: choice.manufacturer.metadata.resourceVersion! } } : undefined);
          }}><option value="">No catalog assignment</option>{values.catalog && !models.some(({ model }) => model.metadata.uid === values.catalog?.uid) && <option value="unavailable" disabled>Selected catalog unavailable · clear or refresh</option>}{models.map(({ model, manufacturer }) => <option key={model.metadata.uid} value={model.metadata.uid}>{catalogLabel(manufacturer, models.map(choice => choice.manufacturer))} · {catalogLabel(model, models.filter(choice => choice.manufacturer.metadata.name === manufacturer.metadata.name).map(choice => choice.model))}</option>)}</select></Label>
          <Hint id={`${id}-catalog`}>Can't be changed later.</Hint>
        </div>
        <div className="inventory-form-grid">
          <div className="inventory-field">
            <Label>Serial number<Input data-problem={problems.serial} maxLength={253} aria-describedby={`${id}-serial`} value={values.serial ?? ''} onChange={event => field('serial', event.target.value)} /></Label>
            <Hint id={`${id}-serial`}>Real serial or system UUID required. Not an asset tag or provider ID.</Hint>
          </div>
          <Label>System UUID<Input data-problem={problems.systemUUID} pattern={systemUUIDPattern} maxLength={36} value={values.systemUUID ?? ''} onChange={event => field('systemUUID', event.target.value)} /></Label>
          {([['assetTag', 'Asset tag'], ['manufacturer', 'Manufacturer'], ['model', 'Model']] as const).map(([name, label]) => <Label key={name}>{label}<Input required={name === 'model'} maxLength={253} value={values[name] ?? ''} onChange={event => field(name, event.target.value)} /></Label>)}
        </div>
      </fieldset>
      <fieldset><legend>Physical components</legend><Hint id={`${id}-components`}>Physical ports and parts only — no interfaces or IPs.</Hint>
        {components.length > 0 && <div className="inventory-editor-scroll"><table className="inventory-editor-table" aria-describedby={`${id}-components`}>
          <thead><tr><th scope="col">ID</th><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Connector</th><th scope="col">Positions</th><th scope="col"><span className="inventory-visually-hidden">Remove</span></th></tr></thead>
          <tbody>{components.map((item, index) => <tr key={index}>
            <td><Input aria-label={`Component ${index + 1} id`} required maxLength={63} data-problem={item.id && components.findIndex(other => other.id === item.id) < index ? 'Component IDs must be unique.' : ''} value={item.id} onChange={event => component(index, { id: event.target.value })} /></td>
            <td><Input aria-label={`Component ${index + 1} name`} required maxLength={253} value={item.name} onChange={event => component(index, { name: event.target.value })} /></td>
            <td><select aria-label={`Component ${index + 1} type`} value={item.type} onChange={event => component(index, { type: event.target.value as PhysicalComponent['type'] })}>{componentTypes.map(type => <option key={type}>{type}</option>)}</select></td>
            <td><Input aria-label={`Component ${index + 1} connector`} maxLength={253} value={item.connector ?? ''} onChange={event => component(index, { connector: event.target.value || undefined })} /></td>
            <td><Input aria-label={`Component ${index + 1} positions`} required type="number" min={1} max={256} value={item.positions ?? ''} onChange={event => component(index, { positions: Number(event.target.value) })} /></td>
            <td><Button type="secondary" theme="outline" size="small" htmlType="button" aria-label={`Remove component ${index + 1}`} onClick={() => field('components', components.filter((_, position) => position !== index))}>×</Button></td>
          </tr>)}</tbody>
        </table></div>}
        <Button type="secondary" theme="outline" size="small" htmlType="button" disabled={components.length >= 256} onClick={() => field('components', [...components, { id: '', name: '', type: 'NetworkPort', positions: 1 }])}>Add component</Button>
      </fieldset>
    </>;
  }
  // Cable ends: pick the device or feed first, then one of its recorded ports.
  const cabled = cabledEndpoints(cables);
  const rackName = (device: Device) => {
    const rack = device.spec.reportedPlacement?.rackRef?.name ?? claims.find(claim => claim.metadata.name === device.spec.claimRef?.name)?.spec.placement?.rackRef?.name;
    // Only readable racks name a group; an unreadable rack's name is not shown.
    const record = places.find(place => place.kind === 'Rack' && place.metadata.name === rack);
    return record ? record.spec.displayName || record.metadata.name : 'Other devices';
  };
  const sources: EndpointSource[] = [];
  for (const record of [...devices, ...feeds]) {
    if (!current(record)) continue;
    const device = 'identity' in record.spec;
    const ports: EndpointSource['ports'] = [];
    for (const component of (device ? (record as Device).spec.components : (record as PhysicalRecord).spec.terminations) ?? []) {
      if (!isTermination(component) || !component.positions) continue;
      for (let position = 1; position <= Math.min(component.positions, 256); position++) {
        const endpoint: ReferencedEndpoint = { kind: device ? 'Device' : 'PowerFeed', name: record.metadata.name, uid: record.metadata.uid!, resourceVersion: record.metadata.resourceVersion!, componentID: component.id, position };
        ports.push({ key: JSON.stringify(endpoint), label: component.positions > 1 ? `${component.name}/${position}` : component.name, cable: cabled.get(endpointKey(endpoint)), endpoint });
      }
    }
    if (ports.length) sources.push({ key: `${device ? 'Device' : 'PowerFeed'}/${record.metadata.uid}`, label: record.spec.displayName || record.metadata.name, group: device ? rackName(record as Device) : 'Power feeds', ports });
  }
  const groups = [...new Set(sources.map(source => source.group))];
  const lanes = values.cableLanes ?? [{ id: 'lane-1' }];
  const lane = (index: number, change: Partial<Lane>) => field('cableLanes', lanes.map((item, position) => position === index ? { ...item, ...change } : item));
  const sourceOf = (item: Lane, side: 'a' | 'b') => item[side] ? `${item[side].kind}/${item[side].uid}` : item[side === 'a' ? 'aSource' : 'bSource'] ?? '';
  const used = lanes.flatMap((item, index) => (['a', 'b'] as const).map(side => ({ index, side, key: item[side] && endpointKey(item[side]) })));
  const repeat = (index: number, side: 'a' | 'b') => {
    const key = lanes[index][side] && endpointKey(lanes[index][side]);
    const first = key && used.find(other => other.key === key);
    if (!first || (first.index === index && first.side === side)) return '';
    return first.index === index ? 'Same port as end A.' : `Port already used by lane ${first.index + 1}.`;
  };
  const nextLane = () => { let number = lanes.length + 1; while (lanes.some(item => item.id === `lane-${number}`)) number++; return `lane-${number}`; };
  return <>
    <Label>Cable medium<select required value={values.medium ?? ''} onChange={event => field('medium', event.target.value)}><option value="">Choose medium</option>{cableMedia.map(medium => <option key={medium}>{medium}</option>)}</select></Label>
    <fieldset><legend>Lanes</legend>
      <div className="inventory-editor-scroll"><table className="inventory-editor-table inventory-lane-table">
        <thead><tr><th scope="col">ID</th><th scope="col">End A</th><th scope="col">End B</th><th scope="col"><span className="inventory-visually-hidden">Remove</span></th></tr></thead>
        <tbody>{lanes.map((item, index) => <tr key={index}>
          <td><Input aria-label={`Lane ${index + 1} ID`} required maxLength={63} data-problem={item.id && lanes.findIndex(other => other.id === item.id) < index ? 'Lane IDs must be unique.' : ''} value={item.id} onChange={event => lane(index, { id: event.target.value })} /></td>
          {(['a', 'b'] as const).map(side => {
            const source = sources.find(candidate => candidate.key === sourceOf(item, side));
            const chosen = source?.ports.find(port => item[side] && port.key === JSON.stringify(item[side]));
            const problem = repeat(index, side);
            const end = `Lane ${index + 1} end ${side.toUpperCase()}`;
            return <td key={side}><div className="inventory-endpoint-picker">
              <select aria-label={`${end} device`} value={source?.key ?? ''} onChange={event => lane(index, { [side]: undefined, [side === 'a' ? 'aSource' : 'bSource']: event.target.value || undefined })}><option value="">Device or feed</option>
                {groups.map(group => <optgroup key={group} label={group}>{sources.filter(candidate => candidate.group === group).map(candidate => <option key={candidate.key} value={candidate.key}>{candidate.label}</option>)}</optgroup>)}
              </select>
              <select aria-label={end} required data-problem={problem} data-attention={problem ? 'conflict' : chosen?.cable ? 'cabled' : undefined} title={problem || (chosen?.cable && `Already cabled: ${chosen.cable}`) || undefined} value={chosen?.key ?? ''} onChange={event => lane(index, { [side]: source?.ports.find(port => port.key === event.target.value)?.endpoint })}><option value="">Port</option>
                {source?.ports.map(port => <option key={port.key} value={port.key} className={port.cable ? 'inventory-option-attention' : undefined} title={port.cable && `Already cabled: ${port.cable}`}>{port.label}{port.cable && ' · cabled'}</option>)}
              </select>
            </div></td>;
          })}
          <td>{lanes.length > 1 && <Button type="secondary" theme="outline" size="small" htmlType="button" aria-label={`Remove lane ${index + 1}`} onClick={() => field('cableLanes', lanes.filter((_, position) => position !== index))}>×</Button>}</td>
        </tr>)}</tbody>
      </table></div>
      <Button type="secondary" theme="outline" size="small" htmlType="button" disabled={lanes.length >= 64} onClick={() => field('cableLanes', [...lanes, { id: nextLane() }])}>Add lane</Button>
    </fieldset>
  </>;
}
