import { describe, expect, it } from 'vitest';
import type { CableEndpoint, Device, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import { toInventory, type Inventory } from '../inventory';
import { deviceConnections, faceGroups, faceLabel, localLabel, oppositeFaceCheck, peerLabel, remotePortLabel, stateCounts, type ConnectionLink } from './device-connections';
import { depthClaims, depthDevices, depthRecords } from './depth-fixtures';

// Synthetic records only.
const NS = 'default';
const C = (id: string, type: PhysicalComponent['type'], positions?: number, name = id): PhysicalComponent => ({ id, name, type, ...(positions ? { positions } : {}) });
const device = (name: string, components: PhysicalComponent[], extra: Partial<Device['spec']> = {}): Device => ({
  metadata: { name, namespace: NS },
  spec: { claimRef: { name: `${name}-claim` }, identity: { model: 'example-model', serial: `SYNTH-${name}` }, displayName: `Example ${name}`, topologyAuthority: 'Cable', components, ...extra },
});
const feed = (name: string, outlets: number): PhysicalRecord => ({
  kind: 'PowerFeed', metadata: { name, namespace: NS },
  spec: { displayName: `Feed ${name}`, supply: { type: 'AC', voltageV: 208, currentA: 30 }, terminations: Array.from({ length: outlets }, (_, index) => C(`outlet-${index + 1}`, 'PowerOutlet', undefined, `Outlet ${index + 1}`)) },
});
const D = (name: string, componentID: string, position = 1): CableEndpoint => ({ deviceRef: { name }, componentID, position });
const P = (name: string, componentID: string, position = 1): CableEndpoint => ({ powerFeedRef: { name }, componentID, position });
const cable = (name: string, medium: string, lanes: [string, CableEndpoint, CableEndpoint][]): PhysicalRecord => ({
  kind: 'Cable', metadata: { name, namespace: NS }, spec: { displayName: name, medium, lanes: lanes.map(([id, a, b]) => ({ id, a, b })) },
});
const inv = (devices: Device[], physical: PhysicalRecord[], issues?: Inventory['issues']): Inventory => ({ ...toInventory([], devices, physical), issues });

const server = device('server-01', [C('psu-1', 'PowerInlet', undefined, 'PSU 1'), C('psu-2', 'PowerInlet', undefined, 'PSU 2'), C('eth0', 'NetworkPort', undefined, 'Ethernet 0'), C('boot-ssd', 'InventoryItem')]);
const leaf = device('leaf-01', [C('swp1', 'NetworkPort'), C('swp2', 'NetworkPort'), C('swp49', 'NetworkPort', 4)]);
const link = (links: ConnectionLink[], componentID: string, position = 1) => links.filter(item => item.componentID === componentID && item.position === position);

describe('deviceConnections', () => {
  it('classifies a single recorded cable as cable recorded, with both ports', () => {
    const result = deviceConnections(server, inv([server, leaf], [cable('dac-01', 'Copper', [['l1', D('server-01', 'eth0'), D('leaf-01', 'swp1')]])]));
    const [eth0] = link(result.links, 'eth0');
    expect(eth0).toMatchObject({ state: 'cabled', group: 'network', cable: { name: 'dac-01', medium: 'Copper', laneID: 'l1' }, peer: { kind: 'Device', access: 'readable', name: 'leaf-01', componentID: 'swp1' } });
    expect(peerLabel(eth0.peer)).toBe('Example leaf-01 · swp1');
    expect(remotePortLabel(eth0.peer)).toBe('swp1');
  });

  it('classifies an uncabled port as no cable recorded, never free', () => {
    const result = deviceConnections(server, inv([server], []));
    const [eth0] = link(result.links, 'eth0');
    expect(eth0.state).toBe('none');
    expect(eth0.cable).toBeUndefined();
    expect(Object.keys(stateCounts(result.links))).not.toContain('free');
    // Items that do not terminate cables are left out unless a Cable names them.
    expect(link(result.links, 'boot-ssd')).toEqual([]);
  });

  it('marks two Cables claiming the same port as records disagree, keeping both', () => {
    const result = deviceConnections(server, inv([server, leaf], [
      cable('dac-01', 'Copper', [['l1', D('server-01', 'eth0'), D('leaf-01', 'swp1')]]),
      cable('dac-02', 'Copper', [['l1', D('server-01', 'eth0'), D('leaf-01', 'swp2')]]),
    ]));
    const claims = link(result.links, 'eth0');
    expect(claims.map(item => [item.state, item.cable?.name])).toEqual([['conflict', 'dac-01'], ['conflict', 'dac-02']]);
    expect(claims[0].reasons.join(' ')).toMatch(/2 Cable lanes name this position/);
  });

  it('marks a far port claimed by another Cable as records disagree', () => {
    const other = device('server-02', [C('eth0', 'NetworkPort')]);
    const result = deviceConnections(server, inv([server, other, leaf], [
      cable('dac-01', 'Copper', [['l1', D('server-01', 'eth0'), D('leaf-01', 'swp1')]]),
      cable('dac-02', 'Copper', [['l1', D('server-02', 'eth0'), D('leaf-01', 'swp1')]]),
    ]));
    const [eth0] = link(result.links, 'eth0');
    expect(eth0.state).toBe('conflict');
    expect(eth0.reasons.join(' ')).toMatch(/peer port is named by 2 Cable lanes: dac-01, dac-02/);
  });

  it('marks a Cable naming a component the Device does not record as records disagree', () => {
    const result = deviceConnections(server, inv([server, leaf], [cable('dac-legacy', 'Copper', [['l1', D('server-01', 'eth9'), D('leaf-01', 'swp2')]])]));
    const [orphan] = link(result.links, 'eth9');
    expect(orphan).toMatchObject({ state: 'conflict', group: 'unrecorded', component: undefined });
    expect(orphan.reasons[0]).toMatch(/names component "eth9", which this Device does not record/);
  });

  it('marks a Cable naming a position above the recorded count as records disagree', () => {
    const result = deviceConnections(server, inv([server, leaf], [cable('dac-03', 'Copper', [['l1', D('server-01', 'eth0', 2), D('leaf-01', 'swp2')]])]));
    expect(link(result.links, 'eth0', 1)[0].state).toBe('none');
    const [orphan] = link(result.links, 'eth0', 2);
    expect(orphan).toMatchObject({ state: 'conflict', group: 'network' });
    expect(orphan.reasons[0]).toMatch(/position 2; this Device records 1/);
  });

  it('marks a peer component the peer does not record as records disagree', () => {
    const result = deviceConnections(server, inv([server, leaf], [cable('dac-01', 'Copper', [['l1', D('server-01', 'eth0'), D('leaf-01', 'swp99')]])]));
    expect(link(result.links, 'eth0')[0]).toMatchObject({ state: 'conflict', reasons: ['The peer does not record component "swp99".'] });
  });

  it('marks a missing peer record as peer unavailable, naming only what the Cable records', () => {
    const result = deviceConnections(server, inv([server], [cable('dac-01', 'Copper', [['l1', D('server-01', 'eth0'), D('leaf-02', 'swp1')]])]));
    const [eth0] = link(result.links, 'eth0');
    expect(eth0.state).toBe('unavailable');
    expect(eth0.peer).toMatchObject({ access: 'missing', name: 'leaf-02' });
    expect(peerLabel(eth0.peer)).toBe('leaf-02 · swp1 (record not found)');
  });

  it('marks a denied peer collection as peer unavailable without leaking its identity', () => {
    const result = deviceConnections(server, inv([server], [
      feed('feed-a', 8),
      cable('pwr-01', 'Power', [['power', P('feed-a', 'outlet-7'), D('server-01', 'psu-1')]]),
    ], { PowerFeed: { status: 403 } }));
    const [psu] = link(result.links, 'psu-1');
    expect(psu.state).toBe('unavailable');
    expect(psu.peer).toEqual({ kind: 'PowerFeed', access: 'denied' });
    expect(JSON.stringify(psu.peer)).not.toMatch(/feed-a|outlet-7/);
    expect(peerLabel(psu.peer)).toBe('Power feed unavailable');
    expect(remotePortLabel(psu.peer)).toBe('Port unavailable');
  });

  it('treats denied Cable reads as unknown connections, not as no cable recorded', () => {
    const result = deviceConnections(server, inv([server], [], { Cable: { status: 403 } }));
    expect(result).toMatchObject({ cableAccess: 'denied', links: [] });
  });

  it('lists DeviceType templates the Device does not record as catalog only', () => {
    const typed = device('server-03', [C('eth0', 'NetworkPort')], { deviceTypeRef: { name: 'example-type' } });
    const physical: PhysicalRecord[] = [
      { kind: 'Manufacturer', metadata: { name: 'example-maker', namespace: NS }, spec: { displayName: 'Example maker', canonicalName: 'Example maker' } },
      { kind: 'DeviceType', metadata: { name: 'example-type', namespace: NS }, spec: { displayName: 'Example type', manufacturerRef: { name: 'example-maker' }, model: 'example-model', componentTemplates: [C('eth0', 'NetworkPort'), C('eth1', 'NetworkPort'), C('psu-1', 'PowerInlet')] } },
    ];
    const result = deviceConnections(typed, inv([typed], physical));
    expect(result.catalog).toBe('compared');
    expect(result.links.filter(item => item.state === 'catalog').map(item => [item.componentID, item.group])).toEqual([['eth1', 'network'], ['psu-1', 'power']]);
    expect(deviceConnections(typed, inv([typed], physical, { DeviceType: { status: 403 } })).catalog).toBe('unavailable');
  });

  it('maps each breakout lane to its own position and peer', () => {
    const servers = [1, 2, 3, 4].map(n => device(`server-1${n}`, [C('eth0', 'NetworkPort')]));
    const breakout = cable('brk-49', 'Copper', [1, 2, 3, 4].map(n => [`lane-${n}`, D('leaf-01', 'swp49', n), D(`server-1${n}`, 'eth0')]));
    const data = inv([leaf, ...servers], [breakout]);
    const result = deviceConnections(leaf, data);
    expect([1, 2, 3, 4].map(n => link(result.links, 'swp49', n)[0]).map(item => [item.state, item.cable?.laneID, item.cable?.laneCount, item.peer?.name])).toEqual([
      ['cabled', 'lane-1', 4, 'server-11'], ['cabled', 'lane-2', 4, 'server-12'], ['cabled', 'lane-3', 4, 'server-13'], ['cabled', 'lane-4', 4, 'server-14'],
    ]);
    expect(localLabel(link(result.links, 'swp49', 3)[0])).toBe('swp49 · position 3');
    const [fromServer] = link(deviceConnections(servers[2], data).links, 'eth0');
    expect(fromServer.peer).toMatchObject({ name: 'leaf-01', componentID: 'swp49', position: 3, multiPosition: true });
    expect(remotePortLabel(fromServer.peer)).toBe('swp49 · position 3');
  });

  it('joins PSU inlets to PowerFeed outlets with the recorded rating, leaving other inlets unknown', () => {
    const result = deviceConnections(server, inv([server], [feed('feed-a', 8), cable('pwr-01', 'Power', [['power', P('feed-a', 'outlet-7'), D('server-01', 'psu-1')]])]));
    const [psu1] = link(result.links, 'psu-1');
    expect(psu1).toMatchObject({ state: 'cabled', group: 'power', peer: { kind: 'PowerFeed', componentName: 'Outlet 7', supply: 'AC · 208 V · 30 A' } });
    expect(link(result.links, 'psu-2')[0].state).toBe('none');
  });

  it('marks a PowerFeed outlet the feed does not record as records disagree', () => {
    const result = deviceConnections(server, inv([server], [feed('feed-a', 2), cable('pwr-01', 'Power', [['power', P('feed-a', 'outlet-7'), D('server-01', 'psu-1')]])]));
    expect(link(result.links, 'psu-1')[0].state).toBe('conflict');
  });

  it('traces a PSU through a PDU outlet-to-inlet path to its feed', () => {
    const pdu = device('pdu-01', [C('inlet', 'PowerInlet', undefined, 'Inlet'), C('out-3', 'PowerOutlet', undefined, 'Outlet 3')], { internalMappings: [{ from: { componentID: 'out-3', position: 1 }, to: { componentID: 'inlet', position: 1 } }] });
    const data = inv([server, pdu], [
      feed('feed-a', 8),
      cable('pwr-srv', 'Power', [['power', D('pdu-01', 'out-3'), D('server-01', 'psu-1')]]),
      cable('pwr-pdu', 'Power', [['power', P('feed-a', 'outlet-1'), D('pdu-01', 'inlet')]]),
    ]);
    const [psu1] = link(deviceConnections(server, data).links, 'psu-1');
    expect(psu1.peer).toMatchObject({ name: 'pdu-01', componentName: 'Outlet 3' });
    expect(psu1.trace).toEqual([expect.objectContaining({ via: { componentID: 'inlet', componentName: 'Inlet', position: 1 }, state: 'cabled', cable: expect.objectContaining({ name: 'pwr-pdu' }), peer: expect.objectContaining({ kind: 'PowerFeed', name: 'feed-a', componentName: 'Outlet 1' }) })]);
    // From the PDU itself, the outlet shows its own internal path.
    const [outlet] = link(deviceConnections(pdu, data).links, 'out-3');
    expect(outlet.internal).toEqual({ componentID: 'inlet', componentName: 'Inlet', position: 1 });
  });

  it('traces a pass-through path and stops with no cable recorded beyond the panel', () => {
    const panel = device('panel-01', [C('front-1', 'FrontPort'), C('rear-1', 'RearPort', 2)], { internalMappings: [{ from: { componentID: 'front-1', position: 1 }, to: { componentID: 'rear-1', position: 2 } }] });
    const data = inv([server, panel], [cable('cat6-01', 'Copper', [['l1', D('server-01', 'eth0'), D('panel-01', 'front-1')]])]);
    const [eth0] = link(deviceConnections(server, data).links, 'eth0');
    expect(eth0.state).toBe('cabled');
    expect(eth0.trace).toEqual([{ via: { componentID: 'rear-1', componentName: 'rear-1', position: 2 }, state: 'none', reasons: [] }]);
  });

  it('stops a trace at an unreadable hop without naming it', () => {
    const pdu = device('pdu-01', [C('inlet', 'PowerInlet'), C('out-3', 'PowerOutlet')], { internalMappings: [{ from: { componentID: 'out-3', position: 1 }, to: { componentID: 'inlet', position: 1 } }] });
    const data = inv([server, pdu], [
      cable('pwr-srv', 'Power', [['power', D('pdu-01', 'out-3'), D('server-01', 'psu-1')]]),
      cable('pwr-pdu', 'Power', [['power', P('feed-hidden', 'outlet-1'), D('pdu-01', 'inlet')]]),
    ], { PowerFeed: { status: 403 } });
    const [psu1] = link(deviceConnections(server, data).links, 'psu-1');
    expect(psu1.trace).toEqual([expect.objectContaining({ state: 'unavailable', peer: { kind: 'PowerFeed', access: 'denied' } })]);
  });

  it('ignores Cables in other namespaces', () => {
    const foreign = { ...cable('dac-x', 'Copper', [['l1', D('server-01', 'eth0'), D('leaf-01', 'swp1')]]), metadata: { name: 'dac-x', namespace: 'other' } };
    expect(link(deviceConnections(server, inv([server, leaf], [foreign])).links, 'eth0')[0].state).toBe('none');
  });
});

describe('component faces in the connection join', () => {
  const faced = toInventory(depthClaims, depthDevices, depthRecords);
  const named = (name: string) => depthDevices.find(record => record.metadata.name === name)!;

  it('inherits chassis faces from templates and derives the rack face from the mounted face', () => {
    const { links } = deviceConnections(named('leaf-01'), faced);
    expect(link(links, 'swp1')[0]).toMatchObject({ chassisFace: 'Front', rackFace: 'rear' });
    expect(link(links, 'psu-1')[0]).toMatchObject({ chassisFace: 'Rear', rackFace: 'front' });
    expect(faceLabel(link(links, 'swp1')[0])).toBe('Chassis front · rack rear');
    const compute = deviceConnections(named('compute-07'), faced).links;
    expect(link(compute, 'eth0')[0]).toMatchObject({ chassisFace: 'Rear', rackFace: 'rear' });
    expect(link(compute, 'console')[0]).toMatchObject({ chassisFace: 'Front', rackFace: 'front' });
  });

  it('keeps the face unknown without a template, and the rack face unknown without a mounted face', () => {
    expect(link(deviceConnections(named('appliance-03'), faced).links, 'net0')[0]).toMatchObject({ chassisFace: undefined, rackFace: undefined });
    const unmounted = { ...named('leaf-01'), spec: { ...named('leaf-01').spec, reportedPlacement: { ...named('leaf-01').spec.reportedPlacement!, face: undefined } } };
    const [swp1] = link(deviceConnections(unmounted, faced).links, 'swp1');
    expect(swp1).toMatchObject({ chassisFace: 'Front', rackFace: undefined });
    expect(faceLabel(swp1)).toBe('Chassis front · rack face unknown');
    expect(faceLabel({})).toBe('Chassis face unknown');
    const hidden = deviceConnections(named('leaf-01'), { ...faced, issues: { DeviceType: { status: 403 } } }).links;
    expect(hidden.every(item => item.chassisFace === undefined && item.rackFace === undefined)).toBe(true);
  });

  it('groups ports by chassis face in the order Front, Rear, Face unknown', () => {
    const groups = faceGroups([{ chassisFace: 'Rear' as const, id: 1 }, { id: 2 }, { chassisFace: 'Front' as const, id: 3 }, { chassisFace: 'Rear' as const, id: 4 }]);
    expect(groups.map(group => [group.title, group.links.map(item => item.id)])).toEqual([['Front', [3]], ['Rear', [1, 4]], ['Face unknown', [2]]]);
    expect(faceGroups([])).toEqual([]);
  });

  it('adds a soft check, never a conflict, when a Cable lands on opposite rack faces', () => {
    const { links } = deviceConnections(named('compute-07'), faced);
    const [eth1] = link(links, 'eth1');
    expect(eth1.state).toBe('cabled');
    expect(eth1.reasons).toEqual([]);
    expect(eth1.peer?.rackFace).toBe('front');
    expect(eth1.checks).toEqual(['Check: this end is on the rack rear and the far end on the rack front. Confirm the records; opposite faces can be correct.']);
    // The reversed leaf's chassis-front ports face the rack rear, like eth0.
    const [eth0] = link(links, 'eth0');
    expect(eth0).toMatchObject({ state: 'cabled', rackFace: 'rear', peer: { rackFace: 'rear' }, checks: [] });
    // Power feeds have no faces.
    expect(link(links, 'psu-1')[0].checks).toEqual([]);
  });

  it('never derives a face for an unreadable peer', () => {
    const { links } = deviceConnections(named('compute-07'), { ...faced, issues: { Device: { status: 403 } } });
    const [eth1] = link(links, 'eth1');
    expect(eth1.peer).toEqual({ kind: 'Device', access: 'denied' });
    expect(eth1.checks).toEqual([]);
  });

  it.each([[undefined, 'rear'], ['front', undefined], ['front', 'front']] as const)('adds no check for %s to %s', (local, far) => {
    expect(oppositeFaceCheck(local, far)).toEqual([]);
  });
});
