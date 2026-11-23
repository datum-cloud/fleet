import { describe, expect, it } from 'vitest';
import { condensedName, sharedValue, withoutContext, withoutLeadingContext } from './display-names';

describe('withoutLeadingContext', () => {
  it('drops a leading context and its separator', () => {
    expect(withoutLeadingContext('Example Facility One · Rack A01', 'Example Facility One')).toBe('Rack A01');
    expect(withoutLeadingContext('Leaf a01 / swp1', 'leaf a01')).toBe('swp1');
  });

  it('leaves names that only share a prefix, or would become empty', () => {
    expect(withoutLeadingContext('Rack A010', 'Rack A01')).toBe('Rack A010');
    expect(withoutLeadingContext('Rack A01', 'Rack A01')).toBe('Rack A01');
    expect(withoutLeadingContext('Rack A01', undefined)).toBe('Rack A01');
  });
});

describe('withoutContext', () => {
  it('removes parts the page already states and empty parts', () => {
    expect(withoutContext(['Example Facility One', 'Rack A01', undefined, false, 'U2'], ['example facility one'])).toEqual(['Rack A01', 'U2']);
  });
});

describe('sharedValue', () => {
  it('returns the common value or undefined', () => {
    expect(sharedValue(['IEC C19', 'IEC C19'])).toBe('IEC C19');
    expect(sharedValue(['IEC C19', 'IEC C13'])).toBeUndefined();
    expect(sharedValue([])).toBeUndefined();
  });
});

describe('condensedName', () => {
  it('shortens known component names and leaves others', () => {
    expect(condensedName('Ethernet 0')).toBe('eth0');
    expect(condensedName('Outlet 3')).toBe('O3');
    expect(condensedName('swp49')).toBe('swp49');
  });
});
