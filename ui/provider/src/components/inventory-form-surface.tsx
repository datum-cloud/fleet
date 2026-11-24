import { Button } from '@datum-cloud/datum-ui/button';
import { Dialog } from '@datum-cloud/datum-ui/dialog';
import { useContext, useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { UNSAFE_DataRouterContext, useBlocker } from 'react-router';
import './physical-inventory.css';

export function confirmInventoryDismissal(dirty: boolean, pending: boolean, uncertain: boolean) {
  if (pending) return false;
  if (uncertain) return window.confirm('The save outcome is unknown. Leaving does not cancel or undo the write. Check inventory before another attempt. Leave this form?');
  return !dirty || window.confirm('Discard unsaved changes?');
}

function RoutedDraftGuard({ dirty, pending, uncertain }: { dirty: boolean; pending: boolean; uncertain: boolean }) {
  const blocker = useBlocker(({ nextLocation }) => (dirty || pending || uncertain) && !/\/(logout|sign-out)(\/|$)/.test(nextLocation.pathname));
  useEffect(() => {
    if (blocker.state !== 'blocked') return;
    const leave = pending ? window.confirm('A save is in progress and its outcome may be unknown. Leaving does not cancel the write. Check inventory before another attempt. Leave this page?') : confirmInventoryDismissal(dirty, false, uncertain);
    if (leave) blocker.proceed();
    else blocker.reset();
  }, [blocker, dirty, pending, uncertain]);
  return null;
}

export function InventoryDraftGuard(props: { dirty: boolean; pending: boolean; uncertain: boolean }) {
  const router = useContext(UNSAFE_DataRouterContext);
  const { dirty, pending, uncertain } = props;
  useEffect(() => {
    if (!dirty && !pending && !uncertain) return;
    const beforeUnload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirty, pending, uncertain]);
  return router ? <RoutedDraftGuard {...props} /> : null;
}

export function InventoryFormSurface({ open, task = false, title, description, trigger, status, hint, onDismiss, children }: {
  open: boolean; task?: boolean; title: string; description?: string; trigger?: ReactNode; status: ReactNode;
  /**
   * Why the trigger is unavailable, as its tooltip rather than a sentence on
   * the page. The trigger points at it with aria-describedby={hint.id}, and
   * the words stay in the accessible text.
   */
  hint?: { id: string; text: string };
  onDismiss: () => void; children: ReactNode;
}) {
  if (task) return <>{status}{open && children}</>;
  return <Dialog open={open} onOpenChange={next => { if (!next) onDismiss(); }}>
    {/* The tooltip sits on the wrapper: a disabled button does not receive the pointer. */}
    <div className="inventory-management-controls" title={hint?.text}><Dialog.Trigger asChild>{trigger}</Dialog.Trigger>{hint && <span id={hint.id} className="inventory-visually-hidden">{hint.text}</span>}</div>
    {!open && status}
    <Dialog.Content className="inventory-dialog">
      <Dialog.Header title={title} description={description} />
      <Dialog.Body className="inventory-dialog-body">{status}{children}</Dialog.Body>
    </Dialog.Content>
  </Dialog>;
}

/**
 * The element a reader sees for a form field. A datum-ui Select validates
 * through a hidden native select; its trigger is the visible control.
 */
function visibleControl(field: Element): HTMLElement {
  if (field instanceof HTMLSelectElement && field.getAttribute('aria-hidden') === 'true') return field.closest('.field-select')?.querySelector<HTMLElement>('[role="combobox"]') ?? field;
  return field as HTMLElement;
}

export function InventoryForm({ children, onSubmit, message }: { children: ReactNode; onSubmit: (event: FormEvent) => void; message: string }) {
  const prefix = useId();
  const summary = useRef<HTMLDivElement>(null);
  const [errors, setErrors] = useState<{ field: HTMLElement; label: string; message: string; id: string }[]>([]);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => { if (message) summary.current?.focus(); }, [message]);
  useEffect(() => { if (attempt && errors.length) summary.current?.focus(); }, [attempt]);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const fields = Array.from(event.currentTarget.elements).filter((field): field is HTMLInputElement | HTMLSelectElement => field instanceof HTMLInputElement || field instanceof HTMLSelectElement);
    // A field states a rule the browser cannot check (such as "serial or system UUID") in data-problem; it is reported like any other invalid field.
    for (const field of fields) { const control = visibleControl(field); if (control.hasAttribute('data-problem')) field.setCustomValidity(control.dataset.problem ?? ''); }
    const invalid = fields
      .filter(field => field.willValidate && !field.validity.valid)
      .map((field, index) => {
        const id = `${prefix}-error-${index}`;
        const control = visibleControl(field);
        control.setAttribute('aria-invalid', 'true');
        control.setAttribute('aria-describedby', id);
        const labels = (control as HTMLButtonElement).labels ?? field.labels;
        const label = control.getAttribute('aria-label') || Array.from(labels?.[0]?.childNodes ?? []).filter(node => node.nodeType === Node.TEXT_NODE).map(node => node.textContent).join('').trim() || 'Field';
        return { field: control, id, label, message: field.validationMessage };
      });
    setErrors(invalid);
    setAttempt(previous => previous + 1);
    if (!invalid.length) onSubmit(event);
  }
  // A typed value raises input; a datum-ui Select raises change on its hidden select.
  const edited = (event: FormEvent<HTMLFormElement>) => {
    const field = visibleControl(event.target as Element);
    field.removeAttribute('aria-invalid');
    field.removeAttribute('aria-describedby');
    setErrors(previous => previous.filter(error => error.field !== field));
  };
  return <form className="inventory-form" noValidate onSubmit={submit} onInput={edited} onChange={edited}>
    {(errors.length > 0 || message) && <div className="inventory-error-summary" tabIndex={-1} ref={summary} role="alert">
      {errors.length > 0 ? <><p>Review the following fields before saving.</p><ul>{errors.map(error => <li key={error.id} id={error.id}><Button type="secondary" theme="link" size="link" htmlType="button" onClick={() => error.field.focus()}>{error.label}: {error.message}</Button></li>)}</ul></> : 'Review the save result before continuing.'}
    </div>}
    {children}
  </form>;
}
