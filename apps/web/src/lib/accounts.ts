import { api } from './api';
import { selectAccount, selectedAccount } from './accountContext';

export interface Capabilities {
  manage_account: boolean;
  create_project: boolean;
  reconcile: boolean;
  read_results: boolean;
  upload: boolean;
}

export interface Organization {
  id: string;
  name: string;
  kind: 'personal' | 'team';
  role: 'owner' | 'admin' | 'member' | 'viewer' | 'uploader' | 'reconciler';
  plan: string;
  capabilities: Capabilities;
}

export interface Accounts {
  user_id: string;
  items: Organization[];
}

/** Reads identity membership independently of a removed account in an old cookie. */
export async function accounts(signal?: AbortSignal): Promise<Accounts> {
  const list = await api<Accounts>('/v1/me/organizations', {
    orgId: null,
    ...(signal ? { signal } : {}),
  });
  signal?.throwIfAborted();
  const previous = selectedAccount();
  const requested =
    typeof window === 'undefined'
      ? null
      : new URLSearchParams(window.location.search).get('account');
  const linked = requested ? list.items.find((item) => item.id === requested) : undefined;
  const selected =
    previous?.userId === list.user_id
      ? list.items.find((item) => item.id === previous.orgId)
      : undefined;
  const initial =
    linked ?? selected ?? list.items.find((item) => item.kind === 'personal') ?? list.items[0];
  if (initial && (!selected || previous?.orgId !== initial.id)) {
    selectAccount(list.user_id, initial.id);
  }
  return list;
}
