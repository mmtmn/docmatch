import { useCallback, useEffect, useState } from 'react';
import { api } from '../../lib/api';
import { t } from '../../lib/i18n';
import styles from './AccountPage.module.css';

type Key = { id: string; label: string; prefix: string; lane: string; scopes: string[] };
type Catalog = {
  items: Key[];
  access: {
    can_create: boolean;
    eligible: boolean;
    limit: number;
    lanes: string[];
    scopes: string[];
  };
};
export function ApiKeysPanel() {
  const [data, setData] = useState<Catalog | null>(null);
  const [label, setLabel] = useState('');
  const [lane, setLane] = useState('api_batch');
  const [scopes, setScopes] = useState<string[]>(['runs:read', 'runs:write', 'uploads:write']);
  const [secret, setSecret] = useState('');
  const [visible, setVisible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [revoking, setRevoking] = useState<string | null>(null);
  const refresh = useCallback(async () => {
    setData(await api<Catalog>('/v1/api-keys'));
  }, []);
  useEffect(() => {
    let active = true;
    api<Catalog>('/v1/api-keys')
      .then((value) => {
        if (active) setData(value);
      })
      .catch((e: unknown) => {
        if (active) setNotice(e instanceof Error ? e.message : t('errors.unknown'));
      });
    return () => {
      active = false;
    };
  }, []);
  async function act(action: () => Promise<void>) {
    setBusy(true);
    setNotice('');
    try {
      await action();
      await refresh();
    } catch (e) {
      setNotice(e instanceof Error ? e.message : t('errors.unknown'));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className={styles.card} aria-labelledby="api-keys-title">
      <h2 id="api-keys-title">{t('api_keys.title')}</h2>
      <p>{t('api_keys.description')}</p>
      {notice && (
        <p role="status" className={styles.notice}>
          {notice}
        </p>
      )}
      {data && (
        <>
          {!data.access.eligible && <p>{t('api_keys.subscription')}</p>}
          <p>{t('api_keys.limit', { count: data.items.length, limit: data.access.limit })}</p>
          {secret && (
            <div className={styles.notice}>
              <label htmlFor="new-api-secret">{t('api_keys.once')}</label>
              <input
                id="new-api-secret"
                type={visible ? 'text' : 'password'}
                value={secret}
                readOnly
                autoComplete="off"
                spellCheck={false}
                className={styles.keySecret}
              />
              <div className={styles.keyActions}>
                <button
                  className={styles.secondary}
                  type="button"
                  onClick={() => setVisible(!visible)}
                >
                  {visible ? t('api_keys.hide') : t('api_keys.show')}
                </button>
                <button
                  className={styles.secondary}
                  type="button"
                  onClick={() => {
                    void navigator.clipboard
                      .writeText(secret)
                      .then(() => setNotice(t('api_keys.copied')))
                      .catch(() => setNotice(t('api_keys.copy_failed')));
                  }}
                >
                  {t('api_keys.copy')}
                </button>
                <button
                  className={styles.secondary}
                  type="button"
                  onClick={() => {
                    setSecret('');
                    setVisible(false);
                  }}
                >
                  {t('api_keys.saved')}
                </button>
              </div>
            </div>
          )}
          {data.access.can_create && (
            <form
              className={styles.keyForm}
              onSubmit={(e) => {
                e.preventDefault();
                void act(async () => {
                  const result = await api<Key & { key: string }>('/v1/api-keys', {
                    method: 'POST',
                    body: { label: label.trim(), scopes, lane },
                  });
                  setSecret(result.key);
                  setVisible(false);
                  setLabel('');
                });
              }}
            >
              <label htmlFor="api-key-label">{t('api_keys.label')}</label>
              <input
                id="api-key-label"
                value={label}
                onChange={(e) => setLabel(e.target.value)}
                maxLength={60}
                required
                disabled={busy}
              />
              <label htmlFor="api-key-lane">{t('api_keys.lane')}</label>
              <select
                id="api-key-lane"
                value={lane}
                onChange={(e) => setLane(e.target.value)}
                disabled={busy}
              >
                {data.access.lanes.map((value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ))}
              </select>
              <fieldset disabled={busy}>
                <legend>{t('api_keys.scopes')}</legend>
                {data.access.scopes.map((scope) => (
                  <label key={scope}>
                    <input
                      type="checkbox"
                      checked={scopes.includes(scope)}
                      onChange={(e) =>
                        setScopes(
                          e.target.checked ? [...scopes, scope] : scopes.filter((v) => v !== scope),
                        )
                      }
                    />{' '}
                    {scope}
                  </label>
                ))}
              </fieldset>
              <button
                className={styles.primary}
                disabled={busy || !label.trim() || !scopes.length || !!secret}
              >
                {t('api_keys.create')}
              </button>
            </form>
          )}
          <ul className={styles.keyList}>
            {data.items.map((key) => (
              <li key={key.id}>
                <strong>{key.label}</strong>
                <p style={{ overflowWrap: 'anywhere' }}>
                  {key.prefix} · {key.lane}
                </p>
                {revoking === key.id ? (
                  <div>
                    <p>{t('api_keys.revoke_confirm', { label: key.label })}</p>
                    <button
                      className={styles.secondary}
                      disabled={busy}
                      onClick={() => {
                        void act(async () => {
                          await api(`/v1/api-keys/${key.id}`, { method: 'DELETE' });
                          setRevoking(null);
                          setSecret('');
                          setNotice(t('api_keys.revoked'));
                        });
                      }}
                    >
                      {t('api_keys.confirm')}
                    </button>
                    <button
                      className={styles.secondary}
                      disabled={busy}
                      onClick={() => setRevoking(null)}
                    >
                      {t('common.cancel')}
                    </button>
                  </div>
                ) : (
                  <button
                    className={styles.secondary}
                    disabled={busy}
                    onClick={() => setRevoking(key.id)}
                  >
                    {t('api_keys.revoke')}
                  </button>
                )}
              </li>
            ))}
          </ul>
          <details>
            <summary>{t('api_keys.usage')}</summary>
            <p>{t('api_keys.usage_text')}</p>
            <pre style={{ overflowX: 'auto' }}>
              {'Authorization: Bearer $DOCMATCH_API_KEY\nContent-Type: application/json\n\nPOST /v1/runs/estimate\n{"sources":[{"upload_id":"upl_…"}],"options":{"lane":"' +
                lane +
                '"}}'}
            </pre>
          </details>
        </>
      )}
    </section>
  );
}
