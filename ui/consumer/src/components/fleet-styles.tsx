// The few styles the host's Tailwind build doesn't include. The plugin has no
// CSS build, so they ship inline, as network-services-operator's NLB pages do.
// Colours come from the host theme's variables, so dark mode follows.
const FLEET_CSS = `
.fleet-band { background: var(--sidebar); }
.fleet-chip { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 8px; border-radius: 6px; border: 1px solid var(--border); background: var(--card); font-family: var(--font-mono); font-size: 11px; line-height: 1; color: var(--card-foreground); white-space: nowrap; }
.fleet-eyebrow { font-size: 0.6875rem; font-weight: 600; letter-spacing: 0.06em; text-transform: uppercase; }
.fleet-fact-label { font-size: 0.625rem; font-weight: 600; letter-spacing: 0.06em; text-transform: uppercase; }
.fleet-accent { color: var(--btn-primary); }
.fleet-dot { display: inline-block; width: 8px; height: 8px; flex-shrink: 0; border-radius: 9999px; background: var(--muted-foreground); }
.fleet-dot[data-tone="success"] { background: var(--success); }
.fleet-dot[data-tone="warning"] { background: var(--warning); }
.fleet-dot[data-tone="danger"] { background: var(--destructive); }
.fleet-note { border-radius: 8px; padding: 10px 12px; background: var(--muted); }
.fleet-note[data-tone="warning"] { background: color-mix(in srgb, var(--warning) 12%, transparent); }
.fleet-note[data-tone="danger"] { background: color-mix(in srgb, var(--destructive) 10%, transparent); }
.fleet-step-mark { display: flex; align-items: center; justify-content: center; width: 20px; height: 20px; flex-shrink: 0; border-radius: 9999px; border: 1px solid var(--border); background: var(--card); color: var(--muted-foreground); }
.fleet-step-mark[data-state="done"] { border-color: var(--success); background: var(--success); color: var(--card); }
.fleet-step-mark[data-state="current"] { border-color: var(--warning); color: var(--warning); background: color-mix(in srgb, var(--warning) 12%, var(--card)); }
.fleet-step-mark[data-state="failed"] { border-color: var(--destructive); background: var(--destructive); color: var(--card); }
.fleet-step-line { height: 2px; flex: 1; border-radius: 1px; background: var(--border); }
.fleet-step-line[data-state="done"] { background: var(--success); }
.fleet-flow { display: grid; gap: 1rem; grid-template-columns: minmax(0, 1fr); align-items: stretch; }
.fleet-arrow { display: none; }
.fleet-steps { display: grid; gap: 1.25rem; grid-template-columns: repeat(2, minmax(0, 1fr)); }
.fleet-profile-grid { display: grid; gap: 0.75rem; grid-template-columns: repeat(auto-fill, minmax(17rem, 1fr)); }
@media (min-width: 48rem) {
  .fleet-steps { grid-template-columns: repeat(4, minmax(0, 1fr)); }
}
@media (min-width: 64rem) {
  .fleet-flow { grid-template-columns: minmax(0, 15rem) 3rem minmax(0, 1fr) 3rem minmax(0, 14rem); gap: 0; }
  .fleet-arrow { display: flex; align-items: center; justify-content: center; }
}`;

export function FleetStyles() {
  return <style>{FLEET_CSS}</style>;
}
