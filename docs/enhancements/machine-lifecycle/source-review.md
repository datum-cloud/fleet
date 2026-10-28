# Source review

Supporting evidence for the [machine lifecycle enhancement](README.md).

## Reviewed sources

The review checked these source revisions on 2026-10-27. Source behavior does not
establish which versions Datum deploys.

- **`milo-os/milo`:** `cd469ff4235c0847d9863a132bdb58f3ff8d3845`
- **`milo-os/ipam`:** `ed3f54151a3f0a08014caec150567ed1012007f2`
- **`milo-os/service-catalog`:** `de3cc40518254f832ad4db4aa104e37bf4ddfe2c`
- **`datum-cloud/network-services-operator`:** `d7fdc43f852450c447d4b7bb44b564c970582b1a`
- **`datum-cloud/cloud`:** `cf31b2b71ded321ae95b411cd97765568aa05cde`

The catalog provisioner's ownership check postdates the imported publication
proposal. This enhancement uses that newer behavior and retains Fleet's separate
provenance and source-use checks. The provider comparison uses public NetActuate
and Servers.com API documentation reviewed on the same date; it does not verify
live catalogs or adapter behavior.

The networking source distinguishes consumer Networks and NetworkInterfaces
from Galactic VPC backing resources. Its current Network admission permits new
IPv6-only networks; address-family fields alone do not establish support. The
Fleet interface schema in the enhancement is proposed, not an existing
networking API.

## Milo integration evidence

Milo routes project requests through:

```text
/apis/resourcemanager.miloapis.com/v1alpha1/projects/{project-name}/control-plane
```

The [project filter][milo-routing] supplies authenticated parent context:

```text
iam.miloapis.com/parent-api-group=resourcemanager.miloapis.com
iam.miloapis.com/parent-type=Project
iam.miloapis.com/parent-name={project-name}
```

The [multicluster provider][milo-multicluster] indexes clients by project name and
can disengage unready projects. Fleet therefore checks root Project UID and
control-plane lifetime separately. These checks remain part of the
[tenancy contract](README.md#milo-project-integration).

## Provider comparison

The published APIs inform the proposed provider boundary:

- **Servers.com:** [Order options][servers-options] depend on location and server
  model. The [creation API][servers-create] combines supported RAM, drive models
  and slots, uplinks, and OS inputs. Its adapter must validate combinations and
  retain concrete IDs; a portable profile cannot encode arbitrary combinations
  as supported merely because each numeric minimum fits.
- **NetActuate:** The API distinguishes [available dedicated devices][net-devices],
  [buying and deploying a device][net-buy], and [deploying an existing
  server][net-build]. This fits separate supply and installation responsibilities.
  Those endpoint descriptions do not establish support for every custom hardware
  combination.

These public API shapes inform the proposed adapters; they do not demonstrate
Fleet's fencing, retry, or cleanup guarantees. Each adapter needs qualification.
The first implementation only matches existing simulated supply. Neither profile
validation nor an unbound claim authorizes procurement. Future acquisition needs
explicit purchasing authority, limits, and cancellation obligations. Consumer
release follows the retained cleaning contract and keeps the asset; it does not
cancel a supplier subscription or dispose of hardware.

[servers-options]: https://www.servers.com/docs/ebm/create-a-dedicated-server-via-public-api/
[servers-create]: https://www.servers.com/docs/api-reference/dedicated-server/create-a-dedicated-server/
[net-devices]: https://www.netactuate.com/docs/api/v2/dedicated-filter-devices
[net-buy]: https://www.netactuate.com/docs/api/v2/dedicated-server-buy-build
[net-build]: https://www.netactuate.com/docs/api/v2/dedicated-server-build

[milo-routing]: https://github.com/milo-os/milo/blob/cd469ff4235c0847d9863a132bdb58f3ff8d3845/pkg/server/filters/projects.go

[milo-multicluster]: https://github.com/milo-os/milo/blob/cd469ff4235c0847d9863a132bdb58f3ff8d3845/pkg/multicluster-runtime/milo/provider.go
