import { useEffect, useState } from 'react';
import { t } from '../../lib/i18n';
import { textLimits, limits } from '../../lib/limits';
import { errorMessage } from '../../lib/display';
import type { projectClient, ProjectRule } from '../../lib/projects';
import { ActionConfirmation } from '../ActionConfirmation';
import styles from './ProjectsApp.module.css';

export function ProjectRules({
  projectId,
  client,
  selected,
  onChange,
  disabled,
}: {
  projectId: string;
  client: ReturnType<typeof projectClient>;
  selected: string[];
  onChange: (ids: string[]) => void;
  disabled: boolean;
}) {
  const [rules, setRules] = useState<ProjectRule[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [confirm, setConfirm] = useState<ProjectRule | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    void client
      .rules(projectId, undefined, abort.signal)
      .then((page) => {
        setRules(page.items);
        setCursor(page.next_cursor ?? null);
      })
      .catch((cause: unknown) => {
        if (!abort.signal.aborted) setError(errorMessage(cause));
      });
    return () => abort.abort();
  }, [client, projectId]);
  const perform = async (action: () => Promise<void>) => {
    setBusy(true);
    setError('');
    try {
      await action();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  };
  const replace = (rule: ProjectRule) =>
    setRules((items) => items.map((item) => (item.id === rule.id ? rule : item)));
  return (
    <details className={styles.manage}>
      <summary>{t('projects.saved_rules')}</summary>
      <p>{t('projects.rules_review_explanation')}</p>
      {error && <p role="alert">{error}</p>}
      {rules.length === 0 && <p>{t('projects.no_saved_rules')}</p>}
      {rules.map((rule) => (
        <details key={rule.id}>
          <summary>
            {rule.title} · {t(`projects.rule_${rule.status}`)} ·{' '}
            {t('projects.rule_version', { version: rule.version })}
          </summary>
          <p className={styles.muted}>
            {rule.provenance.origin === 'user'
              ? t('projects.rule_by_person')
              : t('projects.rule_by_analysis')}
          </p>
          <label className={styles.filter}>
            <input
              type="checkbox"
              checked={selected.includes(rule.id)}
              disabled={
                disabled ||
                busy ||
                rule.status === 'retired' ||
                (!selected.includes(rule.id) && selected.length >= limits.ids.project_rules_max)
              }
              onChange={(event) =>
                onChange(
                  event.target.checked
                    ? [...selected, rule.id]
                    : selected.filter((id) => id !== rule.id),
                )
              }
            />
            {t('projects.rule_select')}
          </label>
          <RuleForm
            key={`${rule.id}:${rule.version}`}
            initial={rule}
            disabled={disabled || busy || rule.status === 'retired'}
            onSave={(title, body) =>
              perform(async () =>
                replace(await client.versionRule(projectId, rule.id, title, body)),
              )
            }
          />
          {rule.status !== 'retired' && (
            <div className={styles.actions}>
              {(rule.status === 'verified' || rule.status === 'candidate') && (
                <button
                  type="button"
                  className="btn btn--ghost"
                  disabled={disabled || busy}
                  onClick={() => setConfirm(rule)}
                >
                  {t(rule.status === 'candidate' ? 'projects.rule_verify' : 'projects.rule_trust')}
                </button>
              )}
              <button
                type="button"
                className="btn btn--ghost"
                disabled={disabled || busy}
                onClick={() => {
                  void perform(async () => {
                    replace(await client.promoteRule(projectId, rule.id, 'retired'));
                    if (selected.includes(rule.id))
                      onChange(selected.filter((id) => id !== rule.id));
                  });
                }}
              >
                {t('projects.rule_retire')}
              </button>
            </div>
          )}
        </details>
      ))}
      {cursor && (
        <button
          type="button"
          className="btn btn--ghost"
          disabled={busy}
          onClick={() => {
            void perform(async () => {
              const page = await client.rules(projectId, cursor);
              setRules((items) => [...items, ...page.items]);
              setCursor(page.next_cursor ?? null);
            });
          }}
        >
          {t('projects.more')}
        </button>
      )}
      <details>
        <summary>{t('projects.rule_new')}</summary>
        <RuleForm
          disabled={disabled || busy}
          onSave={(title, body) =>
            perform(async () => {
              const rule = await client.createRule(projectId, title, body);
              setRules((items) => [rule, ...items]);
            })
          }
        />
      </details>
      {confirm && (
        <ActionConfirmation
          message={t(
            confirm.status === 'candidate'
              ? 'projects.rule_verify_confirm'
              : 'projects.rule_trust_confirm',
            { title: confirm.title },
          )}
          onAnswer={(accepted) => {
            const rule = confirm;
            setConfirm(null);
            if (accepted)
              void perform(async () =>
                replace(
                  await client.promoteRule(
                    projectId,
                    rule.id,
                    rule.status === 'candidate' ? 'verified' : 'trusted',
                  ),
                ),
              );
          }}
        />
      )}
    </details>
  );
}

function RuleForm({
  initial,
  disabled,
  onSave,
}: {
  initial?: ProjectRule;
  disabled: boolean;
  onSave: (title: string, body: string) => Promise<void>;
}) {
  const [title, setTitle] = useState(initial?.title ?? '');
  const [body, setBody] = useState(initial?.body ?? '');
  return (
    <form
      className={styles.settings}
      onSubmit={(event) => {
        event.preventDefault();
        void onSave(title, body);
      }}
    >
      <label>
        {t('projects.rule_title')}
        <input
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          maxLength={textLimits.saved_rule_title}
          required
          disabled={disabled}
        />
      </label>
      <label>
        {t('projects.rule_body')}
        <textarea
          value={body}
          onChange={(event) => setBody(event.target.value)}
          maxLength={textLimits.saved_rule_body}
          rows={4}
          required
          disabled={disabled}
        />
      </label>
      <button
        className="btn btn--ghost"
        disabled={
          disabled ||
          !title.trim() ||
          !body.trim() ||
          (initial?.title === title && initial.body === body)
        }
      >
        {t(initial ? 'projects.rule_save_version' : 'projects.rule_save')}
      </button>
    </form>
  );
}
