import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@datum-cloud/datum-ui/select';
import type { ReactNode } from 'react';

export interface FieldOption {
  value: string;
  label: ReactNode;
  disabled?: boolean;
  /** Why the option is marked or unavailable, as its tooltip. */
  hint?: string;
  /** Amber text: choosing it is allowed but needs a second look. */
  attention?: boolean;
}

export interface FieldOptionGroup { label: string; options: FieldOption[] }

/** The datum-ui Select reserves the empty value for "nothing chosen", so a choice that clears the field uses this one. */
const NONE = '__fleet-none__';

/**
 * A datum-ui Select with the field's value as a plain string: '' is no value.
 * `none` is a choice that clears the field (such as "All facilities");
 * `placeholder` is shown while a required field has no value and is not a choice.
 * Inside a form the Select keeps a hidden native select, so required fields
 * take part in form validation; InventoryForm reports them against the trigger.
 */
export function FieldSelect({ value, onValueChange, options = [], groups = [], none, placeholder, required, disabled, className, ...trigger }: {
  value: string; onValueChange: (value: string) => void;
  options?: FieldOption[]; groups?: FieldOptionGroup[];
  none?: string; placeholder?: string;
  required?: boolean; disabled?: boolean; className?: string;
  id?: string; title?: string;
  'aria-label'?: string; 'aria-describedby'?: string;
  'data-problem'?: string; 'data-attention'?: string;
}) {
  // An option cannot carry the empty value; that is `none`.
  const item = (option: FieldOption) => option.value && <SelectItem key={option.value} value={option.value} data-value={option.value} disabled={option.disabled} title={option.hint} className={option.attention ? 'inventory-option-attention' : undefined}>{option.label}</SelectItem>;
  return <span className={['field-select', className].filter(Boolean).join(' ')}>
    <Select value={value || (none === undefined ? '' : NONE)} onValueChange={next => onValueChange(next === NONE ? '' : next)} required={required} disabled={disabled}>
      <SelectTrigger {...trigger}><SelectValue placeholder={placeholder ?? none} /></SelectTrigger>
      <SelectContent className="field-select-content">
        {none !== undefined && <SelectItem value={NONE} data-value="">{none}</SelectItem>}
        {options.map(item)}
        {groups.filter(group => group.options.length).map(group => <SelectGroup key={group.label}><SelectLabel>{group.label}</SelectLabel>{group.options.map(item)}</SelectGroup>)}
      </SelectContent>
    </Select>
  </span>;
}
