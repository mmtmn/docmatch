import { useEffect, useRef, useState } from 'react';
import type { Organization } from '../../lib/accounts';
import { errorMessage, projectDate } from '../../lib/display';
import { t } from '../../lib/i18n';
import { textLimits } from '../../lib/limits';
import type { RunView } from '../../lib/pipeline';
import { projectClient } from '../../lib/projects';
import styles from './ProjectsApp.module.css';

export function SaveRunAsProject({ org, run }: { org: Organization; run: RunView }) {
  const [name, setName] = useState(
    t('projects.import_name', { date: projectDate(run.created_at ?? '') }),
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);

  const save = async () => {
    if (saving) return;
    const controller = new AbortController();
    request.current = controller;
    setSaving(true);
    setError(null);
    try {
      const project = await projectClient(org.id).importRun(run.id, name, controller.signal);
      if (!controller.signal.aborted) {
        window.location.assign(
          `/app?${new URLSearchParams({ account: org.id, project: project.id })}`,
        );
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(errorMessage(cause));
    } finally {
      if (!controller.signal.aborted) setSaving(false);
    }
  };

  return (
    <details className={styles.settings}>
      <summary>{t('projects.import_action')}</summary>
      <form
        className={styles.create}
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <p>{t('projects.import_explanation', { account: org.name })}</p>
        <label htmlFor={`save-${run.id}`}>{t('projects.name')}</label>
        <input
          id={`save-${run.id}`}
          value={name}
          maxLength={textLimits.project_name}
          required
          disabled={saving}
          onChange={(event) => setName(event.target.value)}
        />
        {error && <p role="alert">{error}</p>}
        <button className="btn btn--primary" type="submit" disabled={saving || !name.trim()}>
          {saving ? t('projects.saving') : t('projects.import_action')}
        </button>
      </form>
    </details>
  );
}
