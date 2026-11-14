// Shared by ui/consumer and ui/provider. Keep this directory free of package
// imports: each plugin has its own node_modules, and a bare import here would
// not resolve from either.

/** The common condition structure from the proposal's status contract. */
export interface Condition {
  type: string;
  status: 'True' | 'False' | 'Unknown';
  observedGeneration?: number;
  lastTransitionTime?: string;
  reason?: string;
  message?: string;
}

export function findCondition(conditions: Condition[], type: string): Condition | undefined {
  return conditions.find((c) => c.type === type);
}

// An absent condition means Unknown, so only an explicit True counts.
export function isConditionTrue(conditions: Condition[], type: string): boolean {
  return findCondition(conditions, type)?.status === 'True';
}
