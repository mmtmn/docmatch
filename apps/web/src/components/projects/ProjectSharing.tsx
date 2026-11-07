import { useEffect, useState } from 'react';
import { t } from '../../lib/i18n';
import { errorMessage } from '../../lib/display';
import { teamClient, type TeamMember } from '../../lib/teams';
import type { ProjectDetail } from '../../lib/projects';
import styles from './ProjectsApp.module.css';

export function ProjectSharing({
  orgId,
  project,
  disabled,
  onShare,
}: {
  orgId: string;
  project: ProjectDetail;
  disabled: boolean;
  onShare: (visibility: ProjectDetail['visibility'], userIds: string[]) => void;
}) {
  const [members, setMembers] = useState<TeamMember[]>([]);
  const [error, setError] = useState('');
  const [visibility, setVisibility] = useState(project.visibility);
  const [users, setUsers] = useState(project.shared_with ?? []);
  useEffect(() => {
    const abort = new AbortController();
    void teamClient(orgId)
      .get(abort.signal)
      .then((team) => setMembers(team.members))
      .catch((cause: unknown) => {
        if (!abort.signal.aborted) setError(errorMessage(cause));
      });
    return () => abort.abort();
  }, [orgId]);
  return (
    <form
      className={styles.settings}
      onSubmit={(event) => {
        event.preventDefault();
        onShare(visibility, visibility === 'team' ? [] : users);
      }}
    >
      <h3>{t('projects.sharing')}</h3>
      <p>{t('projects.sharing_explanation')}</p>
      {error && <p role="alert">{error}</p>}
      <label>
        {t('projects.visibility')}
        <select
          value={visibility}
          disabled={disabled}
          onChange={(event) => setVisibility(event.target.value as ProjectDetail['visibility'])}
        >
          <option value="private">{t('projects.visibility_selected')}</option>
          <option value="team">{t('projects.visibility_team')}</option>
        </select>
      </label>
      {visibility === 'private' && (
        <fieldset disabled={disabled}>
          <legend>{t('projects.sharing_members')}</legend>
          {members
            .filter((member) => member.role !== 'owner' && member.role !== 'admin')
            .map((member) => (
              <label className={styles.filter} key={member.id}>
                <input
                  type="checkbox"
                  checked={users.includes(member.id)}
                  onChange={(event) =>
                    setUsers((items) =>
                      event.target.checked
                        ? [...items, member.id]
                        : items.filter((id) => id !== member.id),
                    )
                  }
                />
                {member.name || member.email} · {member.email}
              </label>
            ))}
        </fieldset>
      )}
      <button className="btn btn--ghost" disabled={disabled || Boolean(error)}>
        {t('projects.sharing_save')}
      </button>
    </form>
  );
}
