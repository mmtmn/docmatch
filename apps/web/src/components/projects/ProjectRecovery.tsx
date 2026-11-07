import { t } from '../../lib/i18n';
import { projectDate } from '../../lib/display';
import type { ProjectDraft } from '../../lib/projectDrafts';
import type { ProjectDetail, ProjectSettings } from '../../lib/projects';
import styles from './ProjectsApp.module.css';

export function ProjectRecovery({
  project,
  drafts,
  disabled,
  onRestore,
  onDiscard,
}: {
  project: ProjectDetail;
  drafts: ProjectDraft[];
  disabled: boolean;
  onRestore: (draft: ProjectDraft) => void;
  onDiscard: (draft: ProjectDraft) => void;
}) {
  if (!drafts.length) return null;
  const settingsText = (settings: ProjectSettings | undefined) => {
    const yesNo = (value: boolean) => t(value ? 'projects.recovery_on' : 'projects.recovery_off');
    return [
      settings?.rules_text || t('projects.recovery_no_rules'),
      `${t('form.use_rules')}: ${yesNo(settings?.use_rules ?? true)}`,
      `${t('form.web_search')}: ${yesNo(settings?.web_search ?? false)}`,
      t('projects.recovery_selected_rules', { count: settings?.rule_ids?.length ?? 0 }),
      ...(settings?.llm_mode ? [t('projects.recovery_mode', { mode: settings.llm_mode })] : []),
    ].join('\n');
  };
  return (
    <details className={styles.recovery}>
      <summary>{t('projects.recovery_title', { count: drafts.length })}</summary>
      <p>{t('projects.recovery_help')}</p>
      {drafts.map((draft) => (
        <section key={draft.id}>
          <h2>
            {t('projects.recovery_date', {
              date: projectDate(new Date(draft.savedAt).toISOString()),
            })}
          </h2>
          {draft.baseVersion !== project.version && <p>{t('projects.recovery_changed')}</p>}
          <div className={styles.recoveryComparison}>
            <div>
              <h3>{t('projects.recovery_server')}</h3>
              {draft.patch.name !== undefined && <p>{project.name}</p>}
              {draft.patch.settings && <pre>{settingsText(project.settings)}</pre>}
            </div>
            <div>
              <h3>{t('projects.recovery_local')}</h3>
              {draft.patch.name !== undefined && <p>{draft.patch.name}</p>}
              {draft.patch.settings && <pre>{settingsText(draft.patch.settings)}</pre>}
            </div>
          </div>
          <button
            type="button"
            className="btn btn--primary"
            disabled={disabled}
            onClick={() => onRestore(draft)}
          >
            {t('projects.recovery_apply')}
          </button>{' '}
          <button
            type="button"
            className="btn btn--ghost"
            disabled={disabled}
            onClick={() => onDiscard(draft)}
          >
            {t('projects.recovery_discard')}
          </button>
        </section>
      ))}
    </details>
  );
}
