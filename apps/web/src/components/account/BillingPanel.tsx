import { useEffect, useRef, useState } from 'react';
import pricing from 'virtual:pricing';
import { PLAN_IDS } from '../../../vite/pricing-schema';
import { api } from '../../lib/api';
import { t, type MessageKey } from '../../lib/i18n';
import styles from './AccountPage.module.css';

type Status = {
  enabled: boolean;
  test_mode: boolean;
  can_manage: boolean;
  has_customer: boolean;
  blocked: boolean;
  minimum_topup_credits: number;
  maximum_topup_credits: number;
  token_brl: number;
  subscription: {
    plan: string;
    status: string;
    current_period_end: string | null;
    cancel_at: string | null;
  } | null;
};
const names = { starter: 'Essencial', pro: 'Plus', business: 'Max' };
const statuses: Record<string, MessageKey> = {
  active: 'billing.active',
  trialing: 'billing.trialing',
  past_due: 'billing.past_due',
  canceled: 'billing.canceled_status',
  unpaid: 'billing.unpaid',
  incomplete: 'billing.incomplete',
  incomplete_expired: 'billing.incomplete_expired',
  paused: 'billing.paused',
};
const date = (value: string) =>
  new Intl.DateTimeFormat('pt-BR', { dateStyle: 'long' }).format(new Date(value));

export function BillingPanel({ onRefresh }: { onRefresh: () => void }) {
  const [status, setStatus] = useState<Status | null>(null);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [quantity, setQuantity] = useState('1000');
  const [selected, setSelected] = useState('');
  const keys = useRef<Record<string, string>>({});
  useEffect(() => {
    let active = true;
    const params = new URLSearchParams(window.location.search);
    api<Status>('/v1/billing/status')
      .then((v) => {
        if (active) {
          setSelected(params.get('plano') ?? '');
          if (params.get('pagamento') === 'sucesso') setNotice(t('billing.waiting'));
          if (params.get('pagamento') === 'cancelado') setNotice(t('billing.canceled'));
          setStatus(v);
        }
      })
      .catch((err: unknown) => {
        if (active) setNotice(err instanceof Error ? err.message : t('errors.unknown'));
      });
    return () => {
      active = false;
    };
  }, []);

  async function purchase(
    kind: 'checkout' | 'topup' | 'portal',
    body?: { plan: string } | { credits: number },
  ) {
    setBusy(true);
    setNotice('');
    const request = kind + JSON.stringify(body ?? {});
    keys.current[request] ??= crypto.randomUUID();
    try {
      const result = await api<{ checkout_url?: string; portal_url?: string }>(
        `/v1/billing/${kind}`,
        {
          method: 'POST',
          ...(body ? { body } : {}),
          idempotencyKey: keys.current[request],
        },
      );
      const target = new URL(result.checkout_url ?? result.portal_url ?? '');
      const host = kind === 'portal' ? 'billing.stripe.com' : 'checkout.stripe.com';
      if (
        target.protocol !== 'https:' ||
        target.host !== host ||
        target.username ||
        target.password
      )
        throw new Error(t('billing.invalid_redirect'));
      window.location.assign(target.href);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : t('errors.unknown'));
      setBusy(false);
    }
  }
  async function refresh() {
    setBusy(true);
    try {
      setStatus(await api<Status>('/v1/billing/status'));
      onRefresh();
      setNotice(t('billing.return_notice'));
    } catch (err) {
      setNotice(err instanceof Error ? err.message : t('errors.unknown'));
    } finally {
      setBusy(false);
    }
  }
  const sub = status?.subscription;
  const hasSubscription = sub && !['canceled', 'incomplete_expired'].includes(sub.status);
  const credits = Number(quantity);
  const validQuantity =
    status &&
    Number.isSafeInteger(credits) &&
    credits >= status.minimum_topup_credits &&
    credits <= status.maximum_topup_credits;
  return (
    <section className={styles.card} aria-labelledby="conta-pagamentos">
      <h2 id="conta-pagamentos">{t('billing.title')}</h2>
      {notice && (
        <p role="status" className={styles.notice}>
          {notice}
        </p>
      )}
      {!status && !notice && <p role="status">{t('billing.loading')}</p>}
      {status && (
        <>
          {!status.enabled && <p>{t('billing.unavailable')}</p>}
          {status.enabled && status.test_mode && (
            <p className={styles.notice}>{t('billing.test_mode')}</p>
          )}
          {status.blocked && (
            <p role="alert">
              {t('billing.blocked')} <a href="/contato">{t('billing.support')}</a>
            </p>
          )}
          {sub && (
            <div>
              <p>
                {t('billing.current', {
                  plan: names[sub.plan as keyof typeof names] ?? sub.plan,
                  status: t(statuses[sub.status] ?? 'billing.incomplete'),
                })}
              </p>
              {sub.current_period_end && (
                <p>{t('billing.period_end', { date: date(sub.current_period_end) })}</p>
              )}
              {sub.cancel_at && <p>{t('billing.cancel_at', { date: date(sub.cancel_at) })}</p>}
            </div>
          )}
          {status.can_manage && status.has_customer && (
            <button
              type="button"
              className={styles.secondary}
              disabled={busy}
              onClick={() => void purchase('portal')}
            >
              {t('billing.manage')}
            </button>
          )}
          {status.enabled && !status.blocked && !hasSubscription && (
            <>
              <p>{t('billing.recurring')}</p>
              <div className={styles.billingPlans}>
                {PLAN_IDS.map((plan) => (
                  <div key={plan} className={styles.billingPlan}>
                    <h3>
                      {names[plan]}{' '}
                      {selected === plan && (
                        <span className={styles.badge}>{t('billing.selected')}</span>
                      )}
                    </h3>
                    <p>
                      {t('billing.monthly', {
                        price: pricing.plans[plan].price_brl,
                        credits: pricing.plans[plan].tokens,
                      })}
                    </p>
                    <button
                      type="button"
                      className={styles.primary}
                      disabled={busy}
                      onClick={() => void purchase('checkout', { plan })}
                    >
                      {t('billing.subscribe', { plan: names[plan] })}
                    </button>
                  </div>
                ))}
              </div>
            </>
          )}
          {status.enabled && !status.blocked && sub?.status === 'active' && (
            <form
              className={styles.topup}
              onSubmit={(event) => {
                event.preventDefault();
                if (validQuantity) void purchase('topup', { credits });
              }}
            >
              <h3>{t('billing.topup_title')}</h3>
              <label htmlFor="billing-quantity">{t('billing.topup_label')}</label>
              <input
                id="billing-quantity"
                type="number"
                min={status.minimum_topup_credits}
                max={status.maximum_topup_credits}
                step="1"
                required
                value={quantity}
                disabled={busy}
                onChange={(e) => setQuantity(e.target.value)}
              />
              <p className={styles.muted}>
                {t('billing.topup_terms', {
                  months: pricing.rollover.topup_tokens_months,
                  minimum: status.minimum_topup_credits,
                })}
              </p>
              {validQuantity && (
                <p>
                  {t('billing.topup_price', {
                    amount: new Intl.NumberFormat('pt-BR', {
                      style: 'currency',
                      currency: 'BRL',
                    }).format(credits * status.token_brl),
                  })}
                </p>
              )}
              <button className={styles.primary} disabled={busy || !validQuantity}>
                {t('billing.topup')}
              </button>
            </form>
          )}
        </>
      )}
      <button
        type="button"
        className={styles.secondary}
        disabled={busy}
        onClick={() => void refresh()}
      >
        {busy ? t('billing.redirecting') : t('billing.refresh')}
      </button>
    </section>
  );
}
