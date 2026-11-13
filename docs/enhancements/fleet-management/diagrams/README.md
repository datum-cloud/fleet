# Fleet enhancement diagrams

These editable SVG sources follow the project boundaries, colors, typography,
and embedded-image approach used by the machine lifecycle enhancement.
They describe proposed contracts, not implemented topology.

- [Inventory resources](inventory-resources.svg): physical identity, placement,
  connection authority, and the separate Metal capacity contract.
- [Control planes](control-planes.svg): canonical project state, Fleet runtime,
  federation delivery, and selected location execution.
- [Admitted work](admitted-work.svg): durable authority before dispatch and
  evidence before readiness.

Edit SVGs directly and retain their accessible titles and descriptions. Keep the
images and the surrounding enhancement text consistent. Solid arrows describe
references or labeled actions; dashed arrows describe returned results, not new
authority. Placement, references, and execution are separate concepts.

Render locally for visual review with `rsvg-convert` (librsvg), for example:

```sh
rsvg-convert docs/enhancements/fleet-management/diagrams/control-planes.svg \
  -o /tmp/fleet-control-planes.png
```

Review labels, arrow directions, spacing, and clipping at the document's display
width. The SVG is both the source and the committed image; temporary PNG previews
do not need to be committed. No remote renderer is required.
