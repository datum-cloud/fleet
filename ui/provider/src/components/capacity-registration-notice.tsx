import { Alert, AlertDescription, AlertTitle } from '@datum-cloud/datum-ui/alert';

export function CapacityRegistrationNotice() {
  return (
    <Alert data-testid="fleet-plugin-capacity-registration-blocked">
      <AlertTitle>Inventory-backed capacity registration is not available yet</AlertTitle>
      <AlertDescription>
        Recording a device does not make it available for allocation. Registration needs a
        server-side identity binding, duplicate-registration protection, and an operator-selected
        cleaning policy contract. Fleet does not yet provide these integrations. A synthetic cleaning
        policy is not a substitute. Keep inventory records in Inventory; do not create a second
        capacity record to work around this restriction. Existing capacity records are unchanged.
      </AlertDescription>
    </Alert>
  );
}
