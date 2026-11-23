// Keeps synthetic fixture data out of production bundles.
//
// VITE_FLEET_FIXTURES is baked into the bundle at build time, so a stray
// variable in the shell or a local .env file would otherwise ship fixtures.
// The flag is only honoured in Vite's `fixtures` mode (`--mode fixtures`, as
// `pnpm build:fixtures`, `pnpm dev:fixtures` and `pnpm screenshots` use).
// Any other build or dev server with the flag set fails instead of silently
// choosing a data source. The image build runs plain `pnpm build`, which is
// production mode, so it can never contain fixtures.

export const FIXTURE_MODE = 'fixtures';

export interface FixtureModeInput {
  command: 'build' | 'serve';
  mode: string;
  /** `vite preview` only serves an existing dist/, so the flag is moot there. */
  isPreview?: boolean;
  flag: string | undefined;
}

/** Returns whether fixture data is compiled in; throws on a contradictory request. */
export function assertFixtureMode({ command, mode, isPreview, flag }: FixtureModeInput): boolean {
  if (isPreview) return false;
  const requested = flag !== undefined && flag !== '' && flag !== 'false';
  if (requested && flag !== 'true') throw new Error(`VITE_FLEET_FIXTURES must be "true" or unset, not "${flag}".`);
  if (requested && mode !== FIXTURE_MODE) {
    throw new Error(`VITE_FLEET_FIXTURES=true is set for a "${mode}" ${command === 'build' ? 'build' : 'dev server'}. Fixture data is only compiled with --mode ${FIXTURE_MODE} (pnpm build:fixtures or pnpm dev:fixtures); unset the variable otherwise.`);
  }
  return requested && mode === FIXTURE_MODE;
}
