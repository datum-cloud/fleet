# Fleet edge import: what to observe, and how

Design input for datum-cloud/fleet #5 (import Omni), #6 (import NetActuate, servers.com, Vultr) and #7 (disagreement report), under datum-cloud/infra#2490. It is shaped so #8 (cluster and node management) and #9 (Talos and Kubernetes rolls) can reuse it without a schema break.

Sources read:

- Fleet PR #1 at `24568e13`, mainly `cluster-lifecycle.md`, `inventory.md` and the `us-east-1` adoption example.
- Fleet PR #2 at `71216c31`, for the `Machine`, `MachineClass`, `MachineClaim`, `MachineProfile` and `Installation` names.
- Fleet `docs/design.md`, `AGENTS.md` and `roadmap.md` on main.
- infra `provisioning/` at local `origin/main` `0478c027` (27 Oct 23:32Z). The importer reads the latest production release tag, not main, and this copy was not fetched, so re-check the seeds against that tag.
- infra#7797 and `docs/guides/fleet-talos-k8s-upgrade.md`.
- Omni `v1.12.2`, which is the `omnictl` version infra pins in `provisioning/install-omnictl`: `client/api/omni/specs/omni.proto`, `client/pkg/omni/resources/omni/labels.go` and `client/pkg/access/role/role.go`.
- gona `v0.3.0`, the version infra pins, and `v0.4.4`.

No credentials were used and no live API was called. Every "live" claim below is either a repo fact or is marked *verify*.

---

## 1. Key choices

1. **Observation-only objects carry an explicit mode.** Each imported object has `spec.managementMode: ObserveOnly`, the field name the proposal already uses on `ServersComMachine`. Admission rejects every other value in milestone 2. Controllers that act (#8, #9) must refuse objects in `ObserveOnly`.
2. **In observe mode, spec holds identity only. Every observed value goes in status.** Omni's *desired* versions (ClusterSpec) and its *actual* versions (MachineStatus) both go in status, in separate blocks.
   - Spec stays empty because a later git-authored spec must not be pre-filled by Omni. That would make Omni the author of Fleet's intent.
   - Git's intent (template pins) is read by the site-records importer and compared in #7. It does not become spec until the hand-over in #8.
3. **Provenance is recorded per source block, not per field.** Each block (`status.omni`, `status.provider`, `status.records`) has `observedAt` and a source condition. A block whose source is stale or unreachable reads **Unknown**. Its last values stay visible as `lastKnown` and are never deleted.
4. **Fleet's core `Machine` is not used in milestone 2.** PR #2's `Machine` is the allocation contract. Creating Machines for occupied hardware risks exposing it as free, which `AGENTS.md` forbids. Instead:
   - Omni machines become `OmniMachine` in group `omni.fleet.datumapis.com`.
   - Rented servers become one kind per vendor: `NetActuateServer` (`netactuate.fleet.datumapis.com`), `ServersComMachine` (`serverscom.fleet.datumapis.com`, the proposal's name) and `VultrServer` (`vultr.fleet.datumapis.com`).
   - Adopting any of them into `Machine`/`MachineClaim` comes later.
5. **Disagreements are objects.** Each is a `Discrepancy` with a deterministic name and labels for site and cluster, so `kubectl get discrepancies -l fleet.datumapis.com/site=us-east-1` answers #7. Each names the check, the sources and the value each source holds.
6. **The importers are read-only twice over.**
   - The credential is read-only on the server side wherever the vendor offers that. Omni's `Reader` role and NetActuate's read-only API keys do.
   - The client is read-only by construction: a narrow Go interface for Omni, and an HTTP transport that allows only GET to an allowlist of paths for providers.
   - Tests prove the client side. The server side is a deployment check.
7. **A Fleet `Site` is a cell is a region**, such as `us-east-1`. It is never a zone (`us-east-1a`) and never a provider facility code.
8. **One kind per vendor, not one normalized server kind.** Each vendor kind carries `spec.managementMode: ObserveOnly` and only the facts that vendor has: BGP sessions on `NetActuateServer`, boot hazards and the OOB address on `ServersComMachine`. Fields every vendor shares come from one embedded Go status struct (§5).
9. **Site records come from the latest infra production release tag**, the highest semver `v*` tag, which is the ref production Flux tracks. The importer never reads `main`.
10. **BGP tells Fleet whether a site is serving.** It is the source for #9's "site stops serving" gate. Which BGP observation to read is still open (§9).

---

## 2. The word "site" means three things today

| Where | "site" means | Example |
|---|---|---|
| infra `DATUM_SITE`, `datum.sites`, MachineClass ids | Datum zone | `us-east-1a` |
| Omni machine label `site`, MachineClass matchers, `sites/netactuate` | provider facility code | `IAD3` (NetActuate), `WAS4` (servers.com) |
| Fleet `Site` (proposal) | region-level placement boundary, also called a cell | `us-east-1` |

Mapping:

| infra term | Fleet field |
|---|---|
| Fleet `Site` | the infra **region** (`us-east-1`) |
| Cell | the same thing as a Fleet `Site` and a region: Site = cell = region |
| Datum zone | `Cluster.spec.zones[]` (`us-east-1a`) |
| Provider facility code | `providerFacilityCode` on `OmniMachine` and on each vendor server kind |

The facility code joins to inventory `Facility` only once Facilities exist; see Open questions. Never store a provider code in a field named `site`.

---

## 3. Entity mapping

Key:

- **Kind**: `Omni-desired` is Omni's own spec. `actual` is observed runtime state. `record` is our git records. `provider` is a provider fact.
- Every Fleet field below is in **status** unless the row says *spec (identity)*.
- **"no field"** means the proposal has no place for the fact.

### 3.1 Site (`fleet.datumapis.com/Site`, one per region)

| Fact | Authoritative source(s) | Fleet field | Kind |
|---|---|---|---|
| Region name | Omni Cluster label `region`; infra `clusters/edge/<region>-*` dirs; `datum.sites` zone list | `metadata.name` | spec (identity) |
| Zones in region | `provisioning/datum.sites` (`netactuate: <zone>,...`) | `status.records.zones[]` (no field; proposal has `Cluster.spec.zones`) | record |
| Regional service VIP | `provisioning/region.ipam` (prod and staging sections) | `status.records.regionalAddresses[]{environment,address}` (no field; owned by IPAM/services, shown read-only) | record |
| City and country | `provisioning/sites/footprint.yaml` `region:` (marked `# verify`) | `status.records.location{city,country}` (proposal uses `locationRef`; no Location importer yet) | record |
| Providers present | footprint `providers[]`; observed `status.providerFacilityCode` on the vendor server kinds | `status.records.providers[]`, `status.observedProviders[]` | record / provider |
| NetActuate facility, region id, ISO per zone | `provisioning/configure-netactuate` (a bash `case`), `sites/netactuate` | `status.records.providerMappings[]{provider,zone,facilityCode,regionID,isoID}` (proposal: adapter mapping to `Facility`) | record |
| Clusters at site | Omni clusters labelled `region`; `clusters/edge/` | `status.clusters[]` | actual |
| Shape profile (`dnsonly` or per-site template) | `provisioning/profiles/<cluster>` or `provisioning/templates/<cluster>/` | `Cluster.status.records.profile` | record |

### 3.2 Cluster (`fleet.datumapis.com/Cluster`, one per Omni cluster)

| Fact | Source | Fleet field | Kind |
|---|---|---|---|
| Name | Omni `Clusters` id | `metadata.name` | spec (identity) |
| Site | Omni Cluster label `region`; name prefix; template `topology-labels` patch | `spec.siteRef` | spec (identity) |
| Provider connection | constant: the one production Omni | `spec.clusterProviderRef` → `OmniInstance/production` | spec (identity) |
| Environment and channel | `clusters/edge/<c>/kustomization.yaml` channel (`channels/edge/stable` or `staging`); name suffix `-staging-lab` | `status.records.channel`, `status.records.environment` (the proposal puts these in spec; in observe mode they are records) | record |
| Pinned Kubernetes version (git) | `templates/<c>/cluster.yaml` or shared `templates/cluster` | `status.records.kubernetesVersion` | record |
| Pinned Talos version (git) | same file, `talos.version` | `status.records.talosVersion` (no field: the proposal pins Talos via `MachinePool.imageRef`, but Omni pins it per cluster) | record |
| Version-floor exception | `provisioning/fleet-floor-exceptions` `<site> <component> <version> <expires>` | `status.records.floorExceptions[]` (no field) | record |
| Omni desired Kubernetes and Talos | `ClusterSpec.kubernetes_version`, `.talos_version` | `status.omni.desired.{kubernetesVersion,talosVersion}` | Omni-desired |
| Omni features | `ClusterSpec.features` (`enable_node_audit_skip`, and others) | `status.omni.features` (no field) | Omni-desired |
| Cluster actual versions | `ClusterStatus.talos_version`, `.kubernetes_version`, `.initial_talos_version` | `status.omni.actual.*` | actual |
| Health | `ClusterStatus.{phase,ready,available,controlplaneReady,kubernetesAPIReady,has_connected_control_planes}` | `status.omni.health` and condition `ProviderReady` | actual |
| Talos roll in progress | `TalosUpgradeStatus.{phase,step,status,error,last_upgrade_version,current_upgrade_version}` | `status.omni.talosUpgrade` (no field; the proposal's `Rollout` is Fleet-driven) | actual |
| Kubernetes roll in progress | `KubernetesUpgradeStatus.{phase,step,status,error,last_upgrade_version,current_upgrade_version}` | `status.omni.kubernetesUpgrade` | actual |
| Upgrade quota per set | `UpgradeRollout.machine_sets_upgrade_quota` | `status.omni.upgradeQuota` | actual |
| Pool counts | `MachineSetStatus.machines` | `status.machinePools[]{name,desired,ready}` (proposal field) | actual |
| Capabilities (Karmada labels) | not Omni; Karmada cluster labels | `status.capabilities` (proposal field), **deferred** | none |
| Serving or drained (BGP routes) | not Omni; **BGP** (decided). Which BGP observation is open | **no field, deferred.** #9 needs it as a gate (infra#7797: us-west-1 drained for 2h48m) | actual |

### 3.3 MachinePool (`fleet.datumapis.com/MachinePool`, one per Omni MachineSet)

| Fact | Source | Fleet field | Kind |
|---|---|---|---|
| Name | Omni `MachineSets` id, verbatim (`us-east-1-alice-serverscom`, `-control-planes`, `-workers`) | `metadata.name` | spec (identity) |
| Cluster | MachineSet label `omni.sidero.dev/cluster` | `spec.clusterRef` | spec (identity) |
| Role | MachineSet label `omni.sidero.dev/role-controlplane` or `role-worker`, plus the set-name suffix (table below) | `spec.role` | spec (identity) |
| Allocation mode | `MachineSetSpec.machine_allocation{name,machine_count,allocation_type}`, or pinned via `MachineSetNode` | `status.omni.allocation{mode: Class|Pinned, omniMachineClass, count}` (no field; Omni MachineClass is **not** Fleet `MachineClass`) | Omni-desired |
| Update strategy | `MachineSetSpec.update_strategy`, `upgrade_strategy_config.rolling.max_parallelism` | `status.omni.updateStrategy` (proposal `updatePolicy.maxConcurrentUpdates` is a later spec) | Omni-desired |
| Template shape (git) | `templates/<c>/workers-*.yaml`, `controlplane.yaml` (class, size, pinned UUIDs, `systemExtensions`) | `status.records.{omniMachineClass,size,pinnedMachines[],systemExtensions[]}` | record |
| Set health | `MachineSetStatus.{phase,ready,error,machines,config_hash,locked_updates}` | `status.replicas`, `status.omni.health` | actual |
| Kubernetes labels and taints | template patches (`nodeLabels`, `registerWithTaints`) | `status.records.labels/taints` (proposal has these in spec; recorded only) | record |

Role mapping (observe mode, from today's templates):

| Omni set suffix | Omni role | Fleet `role` | Today |
|---|---|---|---|
| `control-planes` | controlplane | `controlplane` | NetActuate class `<zone>-controlplane`, size 3 (5 at us-central-1-alice). dnsonly sites use class `<zone>-controlplane-dns`, size 1 |
| `workers` (unnamed set) | worker | `worker` | NetActuate class `<zone>`, size 2. These hold BGP to AS36236 |
| `fabric` | worker | `fabric` | one NetActuate machine, class or pinned |
| `gateway` | worker | `edge` | two servers.com machines, pinned |
| `serverscom` | worker | `compute` | servers.com machines, pinned, with kata extension |

An unknown suffix maps to `role: unknown` and raises a Discrepancy. It is never guessed.

### 3.4 NodeClaim (`fleet.datumapis.com/NodeClaim`, one per Omni ClusterMachine)

| Fact | Source | Fleet field | Kind |
|---|---|---|---|
| Name | Kubernetes node name (`ClusterMachineIdentity.nodename`), prefixed by cluster | `metadata.name` = `<cluster>-<nodename>` | spec (identity) |
| Pool | `ClusterMachine` label `omni.sidero.dev/machine-set` | `spec.poolRef` | spec (identity) |
| Role | from the pool | `spec.role` | spec (identity) |
| Machine bound | ClusterMachine id = Omni machine UUID | `status.omniMachineRef` (no field; the proposal has `claimRef` to MachineClaim, deferred) | actual |
| Node name, IPs | `ClusterMachineIdentity.{nodename,node_ips}` | `status.node.name`, `status.node.addresses[]` (proposal has `status.node.name`) | actual |
| etcd member | `ClusterMachineIdentity.etcd_member_id` | `status.omni.etcdMemberID` (no field; #9 needs it for quorum gates) | actual |
| Stage | `ClusterMachineStatus.stage` (`BOOTING`, `INSTALLING`, `UPGRADING`, `CONFIGURING`, `RUNNING`, `REBOOTING`, `POWERED_OFF`, and others) | `status.omni.stage` | actual |
| Ready, config applied | `ClusterMachineStatus.{ready,config_up_to_date,last_config_error,config_apply_status,is_removed}` | `status.omni.*` and condition `ConfigApplied` | actual |
| Desired Talos version and schematic | `ClusterMachineTalosVersion.{talos_version,schematic_id}` | `status.omni.desired.{talosVersion,schematicID}` | Omni-desired |
| Running Talos version and schematic | `MachineUpgradeStatus.{current_talos_version,current_schematic_id,phase,status,error}`, `MachineStatus.talos_version` | `status.omni.actual.{talosVersion,schematicID,upgradePhase}` (no field) | actual |
| Pending upgrade | `MachinePendingUpdates.upgrade{from_version,to_version,from_schematic,to_schematic}`. **Never read `config_diff`** | `status.omni.pendingUpgrade` | actual |
| Kubelet version, node Ready | `KubernetesStatus.nodes[]{nodename,kubelet_version,ready}` | `status.node.{kubeletVersion,ready}` (proposal has `ready`) | actual |
| Boot identity | `MachineStatusSnapshot.boot_id` | `status.omni.bootID` (detects a reboot or kexec; infra#7797 hang) | actual |
| Hostname in git | template Machine doc patch `machine.network.hostname`; `machine-patches/<c>` ids | `status.records.hostname` | record |
| Datum loopback (IPAM) | template `lo` address (`2607:ed40:10d::1/128`) | `status.records.interfaces[]` (proposal `status.interfaces`; IPAM owns the address) | record |

### 3.5 OmniMachine (`omni.fleet.datumapis.com/OmniMachine`, new, one per Omni machine, assigned or not)

| Fact | Source | Field | Kind |
|---|---|---|---|
| Machine UUID (SMBIOS) | `MachineStatus` id | `metadata.name`, `spec.machineID` | spec (identity) |
| Cluster or unassigned | `MachineStatus.cluster`, `.role`; label `omni.sidero.dev/cluster` | `status.assignment{cluster,machineSet,role}` (empty = **Unassigned**, #5 criterion 2) | actual |
| Connected, maintenance | `MachineStatus.{connected,maintenance}`, `Machine.connected`, label `omni.sidero.dev/connected`/`disconnected` | `status.connected`, `status.maintenance` | actual |
| Power state | `MachineStatus.power_state` (UNSUPPORTED without an infra provider, *verify*) | `status.powerState` | actual |
| Talos version, schematic, extensions, kernel args | `MachineStatus.{talos_version,schematic.full_id,schematic.extensions,schematic.kernel_args,initial_talos_version}` | `status.os.*` | actual |
| Hardware | `MachineStatus.hardware{arch,processors,memory_modules,blockdevices[serial,wwid,system_disk]}` | `status.hardware` (inventory-owned later) | actual |
| Network | `MachineStatus.network{hostname,addresses,default_gateways,network_links[linux_name,hardware_address]}` | `status.network` | actual |
| Platform metadata | `MachineStatus.platform_metadata{platform,instance_id,provider_id,region,zone}` | `status.platform` (instance_id is a join key **only** on cloud platforms) | actual |
| Identity labels | MachineStatus labels `provider`, `site` (from image labels or `MachineLabels`) | `status.labels.provider`, `status.labels.providerFacilityCode` | actual |
| Class matches | evaluate each Omni `MachineClass.match_labels` against the labels | `status.matchingOmniClasses[]` | derived |
| Diagnostics | `MachineStatus.{last_error,diagnostics}` | `status.diagnostics` | actual |
| Bound rented server | join result (§4) | `status.serverRef` (typed: `apiGroup`, `kind`, `name`, where `kind` is `NetActuateServer`, `ServersComMachine` or `VultrServer`), `status.joinEvidence[]` | derived |

### 3.6 Rented servers: one kind per vendor (new, one object per rented server)

| Vendor | Kind | Group |
|---|---|---|
| NetActuate | `NetActuateServer` | `netactuate.fleet.datumapis.com` |
| servers.com | `ServersComMachine` (the proposal's name) | `serverscom.fleet.datumapis.com` |
| Vultr | `VultrServer` | `vultr.fleet.datumapis.com` |

Each kind carries `spec.managementMode: ObserveOnly`. The vendor is the kind, so there is no `spec.provider` field. A fact only one vendor has lives only on that vendor's kind, never as an optional field on a shared type. A cell marked "none" below means that vendor's kind has no such field.

| Fact | NetActuate (gona `Server`) | servers.com (`/hosts`, `/hosts/dedicated_servers/{id}`) | Vultr (govultr `Instance`, `BareMetalServer`) | Field |
|---|---|---|---|---|
| Provider ID | `mbpkgid` | `id` | `id` | `spec.providerID` (identity) |
| Name | `fqdn` (`worker-1161d041-iad3.local`) | `title` (`name` is null) | `label`, `hostname` | `status.name` |
| Facility / location | `location_id`, `city` | location field (*verify name*: `location_code`/`location_id`) | `region` | `status.providerFacilityCode`, `status.providerLocationID` |
| Plan / model | `plan_id`, `package` | `configuration_details.server_model_name`, `configuration` | `plan` | `status.plan` |
| Provider addresses | `ip`, `ipv6` | `public_ipv4_address`, `private_ipv4_address`; IPv6 via networks endpoint (*verify*) | `main_ip`, `v6_main_ip` | `status.addresses[]` (provider-assigned, kept **separate from Datum IPAM**, per #6) |
| Lifecycle status | `status`, `installed` | `status`, `operational_status` (*verify*) | `status`, `server_status` | `status.lifecycle` |
| Power | `state` | `power_status` | `power_status` | `status.power` |
| Billing | `package_billing`, `package_billing_contract_id` | `scheduled_release_at` (*verify*) | `date_created`, `pending_charges` | `status.billing` (operator-only) |
| Boot hazards | none | features `public_ipxe_boot`, `host_rescue_mode`, `oob_public_access` (status only) | none | `ServersComMachine` `status.bootOverrides[]` |
| BGP sessions | `GetBGPSessions(mbpkgid)`: group, ASN, peer IPs, state. **Drop `password`** | none | none | `NetActuateServer` `status.bgpSessions[]` |
| OOB address | none | `oob_ipv4_address` (never the credentials) | none | `ServersComMachine` `status.oobAddress` (operator-only) |
| Bound Omni machine | join (§4) | join | join | `status.omniMachineRef`, `status.joinEvidence[]` |

---

## 4. Identity joins

| Join | Key | Where it holds | Where it is unreliable |
|---|---|---|---|
| Omni machine ↔ NodeClaim | Omni machine UUID = ClusterMachine id | always | none; Omni owns both |
| Omni machine ↔ git template | template `kind: Machine` `name:` UUID; `Workers.machines[]`; `machine-patches` label `omni.sidero.dev/machine` | servers.com machines and pinned NetActuate fabric | class-allocated NetActuate machines are named by **no** template. Their only git trace is `machine-patches/<c>`, and only when they carry a patch |
| servers.com ↔ Omni | **public IPv4**: `public_ipv4_address` ∈ bond0 addresses in `MachineStatus.network.addresses` | strong; static `/31` per box | Omni addresses also include `lo` anycast and fabric `/128`s and `fd00:` bond1 addresses. Match only addresses the provider reports, and require uniqueness |
| servers.com ↔ Omni | **title slug = Talos hostname = node name** (`rename-serverscom-host`, `sc_resolve`) | names follow `{edge|compute}-<6hex>-<location>` | `title` is mutable. The hostname changes only at the next install, so a rename shows as a mismatch until the box is re-imaged |
| servers.com ↔ Omni | SMBIOS UUID (`4c4c4544-…`, Dell) | Omni has it | servers.com API exposes no UUID in the fields infra uses. A Dell UUID may encode the service tag (*verify*). The disk WWID is in templates and MachineStatus, not in the provider API |
| NetActuate ↔ Omni | **fqdn minus `.local` = Talos hostname** (`create-node-netactuate` `name_node`: `<role>-<uuid8>-<providerSite>`) | NetActuate nodes built by the script | a hand-built VM or a DHCP-default `talos-xxxx` hostname breaks it |
| NetActuate ↔ Omni | **public IP** `ip`/`ipv6` ∈ Omni addresses | strong | same anycast caveat as above |
| NetActuate ↔ Omni | `mbpkgid` | none | **Omni never sees it.** It appears only in the `machine-inventory.tsv` workflow artifact, which is ephemeral. The Fleet proposal ledger lists `providerID: 4a724585-…` for a NetActuate machine, but that is the **Omni machine UUID**, not the NetActuate id |
| Vultr ↔ Omni | `platform_metadata.instance_id` if Talos booted with the `vultr` platform; otherwise IP and hostname | unknown | no live Vultr server is known. `create-node-vultr` boots an ISO, which probably leaves the platform as `metal` (*verify*) |
| Omni machine ↔ provider (coarse) | Omni labels `provider`, `site` (stamped by image or `MachineLabels`) | narrows candidates | user labels override image labels, and anyone with Operator can relabel. Use it to cross-check, never as identity |
| Cluster ↔ Site | Omni Cluster label `region`; name `<region>-<name>` | all templates set it | the label exists only if the template set it. Check it against the name |

Join rule:

- Bind when **two independent keys agree** (IP and hostname) and neither key matches another candidate. Record `status.joinEvidence[]` as key, value and source.
- A binding is two typed refs: `OmniMachine.status.serverRef` names the vendor kind (`NetActuateServer`, `ServersComMachine` or `VultrServer`) and object, and the vendor object's `status.omniMachineRef` names the `OmniMachine`. A ref without a kind is never written.
- One key, or conflicting keys, raises an `AmbiguousJoin` Discrepancy and binds nothing.
- Once a binding is made, keep it while either key still agrees. Reassign only when both keys move. This prevents flapping after a rename.

---

## 5. Go types sketch (`fleet.datumapis.com/v1alpha1`)

Only the fields the importers write are shown. Fields the proposal already defines (`identityScopeRef`, `configurationAdmission`, `updatePolicy`, `machineClassRef`, `imageRef`, `consumer`) are **deferred** and are optional, so observe-mode objects validate.

The three vendor server kinds embed one shared Go struct, `RentedServerStatus`, for the fields every vendor has: name, facility, plan, addresses, lifecycle, power and the Omni binding. Each kind adds only its own facts on top. The shared struct is a Go convenience; it is not an API kind and has no group.

```go
type ManagementMode string

const (
	ObserveOnly ManagementMode = "ObserveOnly"
	Managed     ManagementMode = "Managed" // rejected by admission until #8
)

type SourceName string // "omni/production", "netactuate", "serverscom", "vultr", "records/infra"

type SourceObservation struct {
	Source     SourceName  `json:"source"`
	ObservedAt metav1.Time `json:"observedAt"`
	Revision   string      `json:"revision,omitempty"` // Omni resource version, git SHA
}

// FactState is Current, Stale or Unknown; it applies to the whole block.
type FactState string

type Site struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SiteSpec   `json:"spec"`
	Status            SiteStatus `json:"status,omitempty"`
}
type SiteSpec struct {
	ManagementMode ManagementMode `json:"managementMode"`
	// deferred: LocationRef, Allocation.FacilitySelector
}
type SiteStatus struct {
	Records           *SiteRecords          `json:"records,omitempty"`
	Clusters          []string              `json:"clusters,omitempty"`
	ObservedProviders []ProviderFacilityRef `json:"observedProviders,omitempty"`
	Conditions        []metav1.Condition    `json:"conditions,omitempty"` // RecordsCurrent, Consistent
}
type SiteRecords struct {
	SourceObservation `json:",inline"`
	Zones             []string            `json:"zones,omitempty"`
	RegionalAddresses []EnvAddress        `json:"regionalAddresses,omitempty"`
	City, Country     string              // footprint.yaml
	Providers         []string            `json:"providers,omitempty"`
	ProviderMappings  []ProviderZoneMapping `json:"providerMappings,omitempty"`
}

type ClusterSpec struct {
	ManagementMode     ManagementMode         `json:"managementMode"`
	SiteRef            LocalRef               `json:"siteRef"`
	ClusterProviderRef TypedRef               `json:"clusterProviderRef"` // OmniInstance/production
	// deferred: ClusterProviderConfigRef, Channel, Environment, Version, Zones, UpdatePolicy
}
type ClusterStatus struct {
	Records      *ClusterRecords    `json:"records,omitempty"`
	Omni         *OmniClusterObs    `json:"omni,omitempty"`
	MachinePools []PoolCount        `json:"machinePools,omitempty"` // proposal field
	Conditions   []metav1.Condition `json:"conditions,omitempty"`   // ProviderReady, VersionsConverged, Consistent
}
type ClusterRecords struct {
	SourceObservation
	Profile           string // "dnsonly" | "template"
	Channel           string
	Environment       string
	KubernetesVersion string
	TalosVersion      string
	FloorExceptions   []FloorException
}
type OmniClusterObs struct {
	SourceObservation
	State             FactState
	Desired           Versions // ClusterSpec
	Actual            Versions // ClusterStatus
	Health            OmniClusterHealth
	TalosUpgrade      *OmniUpgradeStatus
	KubernetesUpgrade *OmniUpgradeStatus
	UpgradeQuota      map[string]int32
	Features          map[string]bool
}
type Versions struct{ Kubernetes, Talos string }

type MachinePoolSpec struct {
	ManagementMode ManagementMode `json:"managementMode"`
	ClusterRef     LocalRef       `json:"clusterRef"`
	Role           string         `json:"role"` // controlplane|worker|fabric|edge|compute|unknown
	// deferred: MachineClassRef (Fleet MachineClass), ImageRef, Replicas, Labels, Taints, UpdatePolicy, Consumer
}
type MachinePoolStatus struct {
	Records    *PoolRecords // template class, size, pinned UUIDs, systemExtensions
	Omni       *OmniPoolObs // allocation mode, Omni class, count, strategy, phase, config hash
	Replicas   PoolReplicas `json:"replicas"` // proposal field: desired, ready
	Conditions []metav1.Condition
}

type NodeClaimSpec struct {
	ManagementMode ManagementMode `json:"managementMode"`
	PoolRef        LocalRef       `json:"poolRef"`
	Role           string         `json:"role"`
	// deferred: DesiredState, IdentityScopeRef, ConfigurationAdmission
}
type NodeClaimStatus struct {
	Phase          string       `json:"phase,omitempty"` // Observed
	OmniMachineRef *LocalRef    // deferred: ClaimRef, InstallationRef
	Node           *NodeObs     // name, uid, ready, kubeletVersion, addresses
	Omni           *OmniNodeObs // stage, ready, configUpToDate, lastConfigError, desired/actual Talos+schematic,
	                            // upgradePhase, pendingUpgrade, etcdMemberID, bootID
	Records        *NodeRecords // hostname, loopback, install disk WWID from git
	Conditions     []metav1.Condition
}
```

```go
// omni.fleet.datumapis.com/v1alpha1
type OmniMachineSpec struct {
	ManagementMode ManagementMode `json:"managementMode"`
	InstanceRef    LocalRef       `json:"instanceRef"` // OmniInstance
	MachineID      string         `json:"machineID"`   // immutable
}
type OmniMachineStatus struct {
	SourceObservation
	State              FactState
	Assignment         *Assignment // nil = Unassigned
	Connected          *bool
	Maintenance        *bool
	PowerState         string
	OS                 OSObs // talosVersion, schematicID, extensions, kernelArgs
	Network            NetObs
	Hardware           HardwareObs // arch, cpu, memMiB, disks{serial,wwid,systemDisk}
	Platform           PlatformObs
	Labels             IdentityLabels // provider, providerFacilityCode
	MatchingOmniClasses []string
	ServerRef          *TypedRef // NetActuateServer | ServersComMachine | VultrServer
	JoinEvidence       []JoinEvidence
	Conditions         []metav1.Condition
}
```

```go
// shared by the vendor kinds; not an API kind
type RentedServerSpec struct {
	ManagementMode ManagementMode `json:"managementMode"`
	ProviderID     string         `json:"providerID"` // immutable
}
type RentedServerStatus struct {
	SourceObservation
	State                FactState
	Name                 string
	ProviderFacilityCode string
	ProviderLocationID   string
	Plan                 string
	Addresses            []string // provider-assigned only
	Lifecycle, Power     string
	Billing              *BillingObs // operator-only; see open questions
	OmniMachineRef       *TypedRef
	JoinEvidence         []JoinEvidence
	Conditions           []metav1.Condition
}

// netactuate.fleet.datumapis.com/v1alpha1
type NetActuateServerSpec struct{ RentedServerSpec } // providerID = mbpkgid
type NetActuateServerStatus struct {
	RentedServerStatus `json:",inline"`
	BGPSessions        []BGPSessionObs // no secrets
}

// serverscom.fleet.datumapis.com/v1alpha1
type ServersComMachineSpec struct{ RentedServerSpec }
type ServersComMachineStatus struct {
	RentedServerStatus `json:",inline"`
	BootOverrides      []string // features in "activated"
	OOBAddress         string   // operator-only; never the credentials
}

// vultr.fleet.datumapis.com/v1alpha1
type VultrServerSpec struct{ RentedServerSpec }
type VultrServerStatus struct {
	RentedServerStatus `json:",inline"`
}
```

```go
// fleet.datumapis.com/v1alpha1
type Discrepancy struct { // cluster-scoped; labels: site, cluster, check, severity
	Spec   DiscrepancySpec
	Status DiscrepancyStatus
}
type DiscrepancySpec struct {
	Check   string      // stable ID, e.g. "D-ZoneWithoutCluster"
	Subject TypedRef    // the object it is about
	Values  []SourceValue // {source, field, value, observedAt}; "unknown" is a value
}
type DiscrepancyStatus struct {
	Severity        string // Info|Warning|Error
	FirstSeen       metav1.Time
	LastSeen        metav1.Time
	Exemption       *ExemptionRef // matching accepted-exception record, if any
}
```

Rules:

- The importer is the only writer of `status`. Spec is written once at create time, and identity fields are immutable.
- An object is never deleted on a source error. It is marked `Absent` only after **three consecutive successful** lists that do not contain it. Deletion of `Absent` objects is **deferred** to a human decision.
- A Discrepancy is deleted when its check stops firing on fresh data. It is never deleted when the data has merely gone Unknown.

---

## 6. Importer contracts

| | Omni | NetActuate | servers.com | Vultr | Site records (infra git) |
|---|---|---|---|---|---|
| Client | `github.com/siderolabs/omni/client`, pinned to the server minor (infra pins `omnictl v1.12.2`), `client.WithServiceAccount` | gona **≥ v0.4.4** (`redactURL`), never v0.3.0 | plain `net/http`, the paths `lib/serverscom.sh` uses | `govultr/v3` | Flux `GitRepository` artifact of `datum-cloud/infra` at the latest production release tag (highest semver `v*`, the same `ref.semver` production Flux uses), or a shallow clone of that tag. Never `main` |
| Lists | `Clusters`, `ClusterStatuses`, `MachineSets`, `MachineSetStatuses`, `MachineSetNodes`, `ClusterMachines`, `ClusterMachineStatuses`, `ClusterMachineIdentities`, `ClusterMachineTalosVersions`, `MachineStatuses`, `Machines`, `MachineLabels`, `MachineClasses`, `TalosUpgradeStatuses`, `KubernetesUpgradeStatuses`, `KubernetesStatuses`, `MachineUpgradeStatuses`, `MachineStatusSnapshots`, `MachinePendingUpdates` (drop `config_diff`), `ConfigPatches` (**metadata only**; discard `spec.data` on receipt) | `cloud/servers`; `cloud/server?mbpkgid=`; BGP sessions per `mbpkgid`; locations | `GET /hosts?per_page=100` **with pagination** (`sc_resolve` reads only page 1); `GET /hosts/dedicated_servers/{id}`; `GET …/{id}/features` | `Instance.List`, `BareMetalServer.List`, `Region.List` | `provisioning/{datum.sites,region.ipam,sites/*,profiles/*,templates/**,machine-patches/*,fleet/machineclasses/*,fleet-floor-exceptions}`, `clusters/edge/*/kustomization.yaml` |
| Never touch | `Talosconfig`, `Kubeconfig`, `ClusterSecrets`/`MachineSecrets`, `ClusterMachineConfig`, any create, update or delete | create, delete, BGP create, buy/build | `oob_credentials`, any `features/*/activate|deactivate`, `power_cycle`, `ssh_keys`, PUT/POST/DELETE | create, delete, reinstall | none |
| Credential (Secret Manager → ExternalSecret via `gcp-secret-store`) | **new** service account, account role **`Reader`** (`omnictl serviceaccount create fleet-importer --role Reader`). No ACL rule is needed: no Kubernetes access. **Not** `datum-ci` (Admin) | a **read-only** API key, issued for the importer alone. NetActuate offers read-only keys. Never today's `NETACTUATE_API_KEY`, which can buy and delete | read-only token **if offered** (open question). Today's token can wipe hosts via rescue | sub-user with `subscriptions_view` ACL only (*verify*) | Flux deploy key or GitHub App, read-only |
| Read-only proof | the code takes a `Reader` interface with `Get`/`List`/`Watch` only. A test wraps COSI in-memory state in a proxy that fails on `Create`/`Update`/`Destroy`/`AddFinalizer`. A deploy check asserts the SA role is `Reader` | `http.RoundTripper` that allows GET only, plus a path allowlist. A test drives every code path through it | same | same | n/a |
| Secret hygiene | never log ConfigPatch data | **redact `key=` from every URL in errors and logs** in our own transport wrapper, even with gona v0.4.4. Drop `BGPSession.password` | never log the `Authorization` header | never log the key | none |
| Refresh | `Watch` per kind, plus a full relist every 10 min | 15 min | 15 min (a features call per host, so mind rate limits) | 15 min | on a new release tag, checked at least every 10 min |
| Stale after | 3 missed relists → block `Unknown` | 45 min | 45 min | 45 min | 30 min |
| Source down | source condition `Available=False`. Blocks go `Unknown` with last values kept. No object is deleted or created. Discrepancies that depend on the source show the value `unknown` | same | same | same | same |
| Empty answer | a list that parses zero clusters is an **error** (edge always has clusters) | zero servers is an error | zero hosts is an error | zero is valid (no live Vultr known), but the call must have succeeded | zero zones or templates parsed is an error |

The NetActuate GET-only transport and the `key=` redaction stay even with a read-only key. They are defence in depth, no longer the only guard: a read-only key can still leak account data through a logged URL.

Each provider importer writes only its own vendor kind: NetActuate writes `NetActuateServer`, servers.com writes `ServersComMachine`, Vultr writes `VultrServer`.

The records importer reads the release tag, not `main`, because production Flux reads the tag. A change merged to `main` but not yet tagged is not yet a record. The tag is still not exactly what Omni runs: template and pin changes reach Omni only when someone dispatches the cluster sync by hand, so a tagged change can be missing from Omni. Report that gap as a Discrepancy rather than treating the tag as Omni's desired state.

`configure-netactuate` is a bash `case` statement and the importer should not parse it. Move it to a data file first (open question).

---

## 7. Disagreement checks (#7)

Each check reads one or more sources, `R` (records), `O` (Omni) and `P` (provider), and emits a Discrepancy. `P` is a vendor kind: `NetActuateServer`, `ServersComMachine` or `VultrServer`. A Discrepancy's `subject` and values name that kind, never a generic server. A check whose inputs are Unknown emits the value `unknown`. Unknown never counts as healthy.

**Seeds** were taken from infra `origin/main`, not from the production release tag the importer reads. Re-check each seed against the latest tag. Rows marked *verify* rest on an older memory, not current live data.

### 7.1 Records against records

| ID | Contradiction | Sources | Seed today |
|---|---|---|---|
| R1 | Zone in `datum.sites` with no cluster, profile or template | `datum.sites` × `clusters/edge` × `profiles` × `templates` | `us-central-1b`, `us-east-3a` |
| R2 | Regional VIP in `region.ipam` with no cluster | `region.ipam` × `clusters/edge` | `us-east-3` → `67.14.172.31`. ecv keeps us-east-3 on purpose, so this needs an exemption, not a fix |
| R3 | Omni MachineClass file in git that no template references | `fleet/machineclasses` × `templates` | `sjc2-serverscom`, `was4-serverscom`, `us-central-1a-serverscom`. Every servers.com set pins machines by UUID |
| R4 | Footprint disagrees with provider mapping | `sites/footprint.yaml` × `configure-netactuate` × `sites/netactuate` | `br-east-1` (São Paulo) lists no netactuate, but `br-east-1a` maps to NetActuate `GRU`. `providers_catalog` omits servers.com, although we rent at `DFW5`, `WAS4` and `SJC2`. `ca-east-1` is `TOR` in config but `TOR YYZ` in `sites/netactuate` |
| R5 | Footprint `region:` still marked `# verify` | footprint | 16 regions marked |
| R6 | One machine configured in two homes (template Machine doc and `machine-patches`) | `templates/<c>` × `machine-patches/<c>` | `us-west-1-alice` compute `4c4c4544-0039-5a10-8058-b8c04f334434`. Its machine-patches header still says "This cell has no committed template", which is stale |
| R7 | Floor exception expired, pin no longer below the floor, or pin below the floor with no exception | `fleet-floor-exceptions` × `templates/cluster` × per-site pins | `us-central-1-alice talos v1.14.2` expires 2026-11-10, against floor `v1.14.4`. Valid today, fires on 11 Nov |
| R8 | Cluster with both a template and a profile, or neither | `templates` × `profiles` × `clusters/edge` | none today. `sync-cluster` enforces it only at run time |

### 7.2 Omni against records

| ID | Contradiction | Sources | Seed |
|---|---|---|---|
| O1 | Omni cluster with no git record, or git cluster with no Omni cluster | O `Clusters` × `clusters/edge`, `templates`, `profiles` | the upgrade guide warns that templates and `clusters/edge` dirs outlive deleted labs |
| O2 | Omni desired version differs from the git pin. Direction matters: Omni above git means the next sync downgrades | O `ClusterSpec` × R pin | 20 Oct: lab syncs from a stale branch left both labs on Kubernetes 1.35.9 while main pinned 1.36.5 |
| O3 | MachineSet shape differs from the template (class, size, pinned UUIDs) | O `MachineSet`/`MachineSetNode` × R | none |
| O4 | Template pins a machine Omni reports unassigned or absent | R `Workers.machines` × O `MachineStatus.cluster` | *verify*: `compute-2560d9-us-central-1` (`4c4c4544-0035-4210-804b-b4c04f563934`) is pinned in `us-central-1-alice` `workers-serverscom` but was recorded unallocated in Sept |
| O5 | Omni MachineClass not in git, or the reverse; class matching zero or surplus machines | O `MachineClasses` × R × O labels | `fleet/README.md` says live `dfw5-serverscom` was never renamed (*verify*) |
| O6 | Cluster-labelled, machine-scoped ConfigPatch that no template owns, which the next sync deletes (siderolabs/omni#2593) | O `ConfigPatches` metadata × R templates | the rule `sync-cluster` checks only at sync time. This makes it continuous |
| O7 | `machine-patches` entry missing in Omni, or Omni machine patch with no git source | O × R `machine-patches` | none |
| O8 | Machine's Omni `site`/`provider` label not among the facility codes mapped to the cluster's zones | O labels × R mappings | none |
| O9 | Hostname in git differs from Omni hostname or Kubernetes node name | R template `hostname` × O `network.hostname`, `ClusterMachineIdentity.nodename` | none |
| O10 | Unassigned Omni machine (an Info finding with age, never an error) | O | `compute-2560d9-us-central-1` (*verify*) |
| O11 | Assigned machine disconnected, or `config_up_to_date=false`, or `last_config_error` set | O | none |

### 7.3 Provider against Omni and records

| ID | Contradiction | Sources | Seed |
|---|---|---|---|
| P1 | Provider server with no Omni machine: we pay for it and it runs nothing | P × O join | WAS1 boxes flagged `retirementRequested` in the proposal ledger (*verify* they still exist) |
| P2 | Omni machine labelled `provider=X` with no server at X | O labels × P | none |
| P3 | Provider location differs from the Omni `site` label or from the cluster's Site | P facility × O label × R mapping | none |
| P4 | Provider address not on the bound Omni machine | P addresses × O addresses | none |
| P5 | servers.com `title` differs from the Omni hostname: rename not yet applied by a re-image | `ServersComMachine` title × O hostname | `rename-serverscom-host` documents this state |
| P6 | Provider says off or stopped while Omni says connected, or provider running while Omni is disconnected for more than 10 min (a hung node) | P power × O connected/stage | infra#7797: `worker-574abe73-sjc` froze mid-kexec |
| P7 | servers.com `public_ipxe_boot` or `host_rescue_mode` activated on an assigned machine. The next reboot during a roll boots iPXE or rescue | `ServersComMachine` boot overrides × O assignment | README: "public_ipxe_boot is PERSISTENT" |
| P8 | Release or cancellation scheduled on an assigned machine | P billing × O assignment | none |
| P9 | NetActuate BGP session expected for the role (`bgp-worker`, `bgp-controlplane-dns`) but missing or down, or a session on a server with no node | `NetActuateServer` BGP sessions × O role | none |
| P10 | Plan memory differs from the Omni class memory guard (`omni.sidero.dev/mem`) | P plan × O class | none |
| P11 | One Omni machine joined by two vendor server objects, or one server by two machines (`AmbiguousJoin`) | P × O join, across all three vendor kinds | none |

### 7.4 Upgrade readiness (feeds #9; Omni and records)

| ID | Contradiction | Sources |
|---|---|---|
| U1 | Mixed Talos or kubelet versions in one cluster for more than 2h with no `TalosUpgradeStatus`/`KubernetesUpgradeStatus` Upgrading | O |
| U2 | Machine schematic extensions differ from the set's template `systemExtensions` (kata). Omni would reinstall the machine on join | O schematic × R |
| U3 | Kubelet version differs from Omni desired Kubernetes version after the roll reports Done | O |
| U4 | Single compute node at a site, so a reboot restarts every workload (infra#7797, us-west-1) | O pools × role. Info |
| U5 | Control plane with even size or below 3 at a non-dnsonly cluster | O. `us-central-1-alice` has 5, fine. dnsonly sites run 1 by design, so this needs an exemption |

**Exemptions.** Accepted states such as us-east-3, dnsonly CP size 1 and `compute-2560d9` held in reserve need a record. Reuse the `fleet-floor-exceptions` shape: `<check> <subject> <reason> <expires>`. An exemption lowers the severity to Info. It never hides the Discrepancy.

---

## 8. What #8 and #9 inherit

- Per node: desired and actual Talos version and schematic, kubelet version, stage, `config_up_to_date`, `bootID`, etcd member, connected, provider power. That is enough to gate "one node at a time, wait until it serves" and to report per-node progress (#7797 follow-up: "report each node's own state").
- Per pool: role, Omni allocation mode, rolling `max_parallelism`. Per cluster: Omni upgrade phase and step, plus records channel or environment for "staging labs first".
- **Missing, deferred:** site serving state (BGP withdrawn or restored) and per-site compute capacity. BGP is the chosen source for "site stops serving", but #9 cannot gate on it until the BGP observation to read is chosen (§9).

---

## 9. Open questions for a human

1. **Read-only servers.com and Vultr credentials.** Does servers.com issue read-only API tokens, and is a Vultr sub-user with `subscriptions_view` only truly read-only (*verify*)? If not, the importer holds a token that can wipe hosts (servers.com rescue). Decide whether the client-side GET-only transport is an acceptable guard for those two, or whether #6 waits for vendor-side scoping. NetActuate is settled: it offers read-only keys.
2. **Fleet `Machine` in milestone 2, or later?** This design defers it and uses `OmniMachine`. PR #2 may want imported capacity as `Machine` with `allocationPolicy: Disabled`.
3. **Inventory `Facility` objects.** Do we create inventory `Facility` objects for provider codes now, or keep `providerFacilityCode` as a plain string until inventory lands?
4. **NodeClaim naming for adopted nodes.** This design uses `<cluster>-<nodename>`. NetActuate node names embed a per-VM random id, so a replacement VM changes the name. Is that acceptable for observe mode, given the proposal's logical-slot names (`…-compute-0`)?
5. **Billing data.** Should Fleet hold billing and release dates, which P1 and P8 need, and who may read them?
6. **Which BGP observation.** BGP is the source for #9's "site stops serving" gate. Which observation does Fleet read: route-restore state, BGP sessions in VictoriaMetrics, Cilium BGP CRs via Karmada, or NetActuate BGP session state on `NetActuateServer`? And should `kube_node_info` be a further source next to Omni?
7. **`configure-netactuate` and other bash-held facts.** Move the zone → NetActuate region/ISO/facility map into a data file before the importer reads it?
8. **Where exemptions live.** In the infra repo next to `fleet-floor-exceptions`, or in the fleet repo as Fleet objects?
9. **Absent objects.** After a source positively reports an object gone, who deletes the Fleet record, and when?
