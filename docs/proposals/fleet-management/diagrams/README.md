# Maintaining the Fleet diagrams

The diagrams support the [service proposal](../README.md),
[service architecture](../architecture.md), and
[bare-metal provisioning design](../bare-metal-provisioning.md#architecture).
Introduce the behavior and responsibilities in the document before showing the
diagram that explains how the services support them.

Sources use C4 system context and container notation (C1 and C2). Keep them at
the level of people, software systems, services, and stores. Code structure,
individual resource kinds, and physical rack layouts belong in other documents.
Use titles and captions that describe the system behavior being explained.

- [Fleet and the platform](context.puml) introduces the users and neighboring
  systems supporting the service. [Rendered diagram](context.png).
- [How Fleet services coordinate](containers.puml) shows the responsibilities
  involved in fulfilling infrastructure requests. [Rendered diagram](containers.png).
- [Operating owned servers](provisioning-containers.puml) shows the execution
  and access services supporting the provisioning lifecycle.
  [Rendered diagram](provisioning-containers.png).

Blue boxes belong to Fleet; gray systems sit outside the Fleet service boundary,
including shared platform services. Arrows describe interactions. Orange arrows
carry admitted effect authority for a specific binding; dashed gray arrows carry
observations. Observed progress does not grant permission for another effect.

Render the PNG files locally from these sources using the C4 library bundled
with PlantUML. From this directory:

```sh
plantuml -tpng context.puml containers.puml provisioning-containers.puml
```

Commit the sources and regenerated images together. Check the rendered diagrams
for readable labels and relationships; no external includes or remote rendering
service are required.
