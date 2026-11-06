// Only the selected destination lives here. Credentials stay in HttpOnly cookies.
// sessionStorage is per tab; every operation captures this value before it starts.
export interface AccountSelection {
  userId: string;
  orgId: string;
}

const KEY = 'docmatch:account:v1';
export const ACCOUNT_CHANGED_EVENT = 'docmatch:account-changed';
let selection: AccountSelection | null = null;

export function selectedAccount(): AccountSelection | null {
  if (typeof window === 'undefined') return null;
  if (selection) return selection;
  try {
    const value: unknown = JSON.parse(window.sessionStorage.getItem(KEY) ?? 'null');
    if (
      value &&
      typeof value === 'object' &&
      'userId' in value &&
      'orgId' in value &&
      typeof value.userId === 'string' &&
      typeof value.orgId === 'string' &&
      /^usr_[0-9A-Z]{26}$/.test(value.userId) &&
      /^org_[0-9A-Z]{26}$/.test(value.orgId)
    )
      selection = { userId: value.userId, orgId: value.orgId };
  } catch {
    /* Storage may be unavailable in a private window. */
  }
  return selection;
}

export function selectAccount(userId: string, orgId: string): void {
  selection = { userId, orgId };
  if (typeof window === 'undefined') return;
  try {
    window.sessionStorage.setItem(KEY, JSON.stringify(selection));
  } catch {
    /* Memory fallback. */
  }
  window.dispatchEvent(new Event(ACCOUNT_CHANGED_EVENT));
}

export function clearAccountSelection(): void {
  selection = null;
  if (typeof window === 'undefined') return;
  try {
    window.sessionStorage.removeItem(KEY);
  } catch {
    /* Memory fallback. */
  }
}
