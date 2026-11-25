// The Device page's power reads: power operations, custody, the caller's
// identity and the power operator permission. Tests that count inventory
// reads count these apart.
export function isPowerRead(url: string, init?: RequestInit) {
  return /\/(devicepoweroperations|devicecustodies)\b|selfsubjectreviews/.test(url) || (url.includes('selfsubjectaccessreviews') && String(init?.body ?? '').includes('"devicepoweroperations"'));
}
