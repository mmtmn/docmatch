import { useEffect, useMemo, useState } from 'react';
import { accounts } from '../../lib/accounts';
import { selectedAccount } from '../../lib/accountContext';
import { t, type MessageKey } from '../../lib/i18n';
import { textLimits } from '../../lib/limits';
import { teamClient, teamError, type Team, type SeatRole, type Invitation } from '../../lib/teams';
import { ActionConfirmation } from '../ActionConfirmation';
import { projectDate } from '../../lib/display';
import styles from '../projects/ProjectsApp.module.css';

const statusLabels: Record<Invitation['status'], MessageKey> = {
  pending: 'teams.pending',
  accepted: 'teams.accepted',
  declined: 'teams.declined',
  expired: 'teams.expired',
  revoked: 'teams.revoked',
};
const roleLabels: Record<Team['role'], MessageKey> = {
  owner: 'teams.owner',
  admin: 'teams.admin',
  member: 'teams.legacy_member',
  viewer: 'teams.viewer',
  uploader: 'teams.upload_only',
  reconciler: 'teams.full_access',
};

export function TeamPage() {
  const [context, setContext] = useState<{ userId: string; team: Team } | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    void accounts(signal)
      .then(async (list) => {
        const orgId = selectedAccount()?.orgId;
        if (!orgId) throw new Error(t('projects.no_account'));
        const team = await teamClient(orgId).get(signal);
        if (!signal.aborted) setContext({ userId: list.user_id, team });
      })
      .catch((cause: unknown) => {
        if (!signal.aborted) setError(teamError(cause));
      });
    return () => controller.abort();
  }, []);
  if (error)
    return (
      <section className={styles.page}>
        <p role="alert">{error}</p>
        <a href="/login">{t('nav.login')}</a>
      </section>
    );
  if (!context) return <p role="status">{t('mobile.loading')}</p>;
  return <TeamSettings initial={context.team} userId={context.userId} />;
}

function TeamSettings({ initial, userId }: { initial: Team; userId: string }) {
  const client = useMemo(() => teamClient(initial.id), [initial.id]);
  const [team, setTeam] = useState(initial);
  const [name, setName] = useState(initial.name);
  const [email, setEmail] = useState('');
  const [role, setRole] = useState<SeatRole>('uploader');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState('');
  const [confirm, setConfirm] = useState<{
    message: string;
    action: () => Promise<void>;
    refresh: boolean;
  } | null>(null);
  const perform = async (action: () => Promise<void>, refresh = true) => {
    setBusy(true);
    setError(null);
    setNotice('');
    try {
      await action();
      if (refresh) setTeam(await client.get());
    } catch (cause) {
      setError(teamError(cause));
    } finally {
      setBusy(false);
    }
  };
  const ask = (message: string, action: () => Promise<void>, refresh = true) =>
    setConfirm({ message, action, refresh });
  return (
    <section className={styles.page}>
      <nav className={styles.navigation} aria-label={t('projects.navigation')}>
        <a href="/app">{t('projects.title')}</a>
        {team.capabilities.read_results && <a href="/atividade">{t('projects.activity')}</a>}
        <a href="/equipe" aria-current="page">
          {t('teams.title')}
        </a>
        <a href="/conta">{t('teams.profile_plan')}</a>
      </nav>
      <header className={styles.heading}>
        <div>
          <h1>{t('teams.title')}</h1>
          <p>{team.kind === 'personal' ? t('teams.personal_intro') : team.name}</p>
        </div>
        <span className={styles.muted}>{t(roleLabels[team.role])}</span>
      </header>
      {error && <p role="alert">{error}</p>}
      {notice && (
        <p className={styles.notice} role="status">
          {notice}
        </p>
      )}
      {team.kind === 'personal' ? (
        <div className={styles.create}>
          <h2>{t('teams.convert_title')}</h2>
          <p>{t('teams.convert_body')}</p>
          <p>{t('teams.no_charge')}</p>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              ask(
                t('teams.convert_confirm', { name }),
                async () => {
                  await client.convert(name);
                  window.location.assign(`/equipe?account=${encodeURIComponent(team.id)}`);
                },
                false,
              );
            }}
          >
            <label htmlFor="team-name">{t('teams.name')}</label>
            <div className={styles.actions}>
              <input
                id="team-name"
                value={name}
                maxLength={textLimits.company_name}
                required
                onChange={(event) => setName(event.target.value)}
                disabled={busy}
              />
              <button
                className="btn btn--primary"
                disabled={busy || !name.trim() || team.role !== 'owner'}
              >
                {t('teams.convert')}
              </button>
            </div>
          </form>
          <p>
            <a href="/conta">{t('teams.profile_plan')}</a>
          </p>
        </div>
      ) : (
        <>
          <p className={styles.notice}>{t('teams.no_charge')}</p>
          {team.capabilities.manage_account ? (
            <>
              <form
                className={styles.create}
                onSubmit={(event) => {
                  event.preventDefault();
                  void perform(async () => {
                    await client.invite(email, role);
                    setEmail('');
                    setNotice(t('teams.invite_sent'));
                  });
                }}
              >
                <h2>{t('teams.invite')}</h2>
                <label htmlFor="seat-email">{t('teams.email')}</label>
                <input
                  id="seat-email"
                  type="email"
                  autoComplete="email"
                  value={email}
                  maxLength={textLimits.email}
                  required
                  disabled={busy || !team.can_invite}
                  onChange={(event) => setEmail(event.target.value)}
                />
                <fieldset className={styles.settings} disabled={busy || !team.can_invite}>
                  <legend>{t('teams.permission')}</legend>
                  <label className={styles.filter}>
                    <input
                      type="radio"
                      name="seat-role"
                      checked={role === 'uploader'}
                      onChange={() => setRole('uploader')}
                    />
                    {t('teams.upload_only')}
                  </label>
                  <p className={styles.muted}>{t('teams.upload_explanation')}</p>
                  <label className={styles.filter}>
                    <input
                      type="radio"
                      name="seat-role"
                      checked={role === 'reconciler'}
                      onChange={() => setRole('reconciler')}
                    />
                    {t('teams.full_access')}
                  </label>
                  <p className={styles.muted}>{t('teams.full_explanation')}</p>
                </fieldset>
                <button
                  className="btn btn--primary"
                  disabled={busy || !team.can_invite || !email.trim()}
                >
                  {t('teams.send_invite')}
                </button>
                <p className={styles.muted}>{t('teams.accept_required')}</p>
                {!team.can_invite && <p>{t('teams.subscription_required')}</p>}
              </form>
              <h2>{t('teams.members')}</h2>
              <p className={styles.muted}>
                {t('teams.members_limit', { count: team.members.length, max: team.member_limit })}
              </p>
              <ul className={styles.documents}>
                {team.members.map((member) => (
                  <li key={member.id}>
                    <div>
                      <strong>
                        {member.name || member.email}
                        {member.id === userId ? ` · ${t('teams.you')}` : ''}
                      </strong>
                      <small>{member.email}</small>
                      <small>{t(roleLabels[member.role])}</small>
                    </div>
                    {member.id !== userId && member.role !== 'owner' && (
                      <div className={styles.actions}>
                        {member.role !== 'admin' && (
                          <label>
                            <span className="visually-hidden">
                              {t('teams.permission_for', { email: member.email })}
                            </span>
                            <select
                              value={member.role}
                              disabled={busy}
                              onChange={(event) => {
                                const value = event.target.value as SeatRole;
                                ask(
                                  t('teams.change_role_confirm', {
                                    email: member.email,
                                    role: t(
                                      value === 'uploader'
                                        ? 'teams.upload_only'
                                        : 'teams.full_access',
                                    ),
                                  }),
                                  () => client.changeRole(member.id, value),
                                );
                              }}
                            >
                              {member.role === 'member' && (
                                <option value="member">{t('teams.legacy_member')}</option>
                              )}
                              {member.role === 'viewer' && (
                                <option value="viewer">{t('teams.viewer')}</option>
                              )}
                              <option value="uploader">{t('teams.upload_only')}</option>
                              <option value="reconciler">{t('teams.full_access')}</option>
                            </select>
                          </label>
                        )}
                        {(member.role !== 'admin' || team.role === 'owner') && (
                          <button
                            type="button"
                            className="btn btn--ghost"
                            disabled={busy}
                            onClick={() =>
                              ask(t('teams.remove_confirm', { email: member.email }), () =>
                                client.remove(member.id),
                              )
                            }
                          >
                            {t('teams.remove')}
                          </button>
                        )}
                        {team.role === 'owner' &&
                          (member.role === 'reconciler' || member.role === 'admin') && (
                            <button
                              type="button"
                              className="btn btn--ghost"
                              disabled={busy}
                              onClick={() =>
                                ask(t('teams.transfer_confirm', { email: member.email }), () =>
                                  client.transfer(member.id),
                                )
                              }
                            >
                              {t('teams.transfer')}
                            </button>
                          )}
                      </div>
                    )}
                  </li>
                ))}
              </ul>
              <h2>{t('teams.invitations')}</h2>
              {team.invitations.length === 0 ? (
                <p>{t('teams.no_invitations')}</p>
              ) : (
                <ul className={styles.documents}>
                  {team.invitations.map((invitation) => (
                    <li key={invitation.id}>
                      <div>
                        <strong>{invitation.email}</strong>
                        <small>
                          {t(statusLabels[invitation.status])} · {t(roleLabels[invitation.role])}
                        </small>
                        {invitation.status === 'pending' && (
                          <small>
                            {t('teams.expires', { date: projectDate(invitation.expires_at) })}
                          </small>
                        )}
                      </div>
                      {(invitation.status === 'pending' || invitation.status === 'expired') && (
                        <div className={styles.actions}>
                          <button
                            type="button"
                            className="btn btn--ghost"
                            disabled={busy || !team.can_invite}
                            onClick={() => {
                              void perform(async () => {
                                await client.resend(invitation.id);
                                setNotice(t('teams.resent'));
                              });
                            }}
                          >
                            {t('teams.resend')}
                          </button>
                          <button
                            type="button"
                            className="btn btn--ghost"
                            disabled={busy}
                            onClick={() =>
                              ask(t('teams.revoke_confirm', { email: invitation.email }), () =>
                                client.revoke(invitation.id),
                              )
                            }
                          >
                            {t('teams.revoke')}
                          </button>
                        </div>
                      )}
                    </li>
                  ))}
                </ul>
              )}
              <p>
                {t('teams.sharing_reminder')} <a href="/app">{t('projects.title')}</a>
              </p>
            </>
          ) : (
            <p>
              {team.role === 'uploader'
                ? t('teams.upload_explanation')
                : t('teams.full_explanation')}
            </p>
          )}
          <details className={styles.manage}>
            <summary>{t('teams.leave')}</summary>
            {team.role === 'owner' ? (
              <p>{t('teams.owner_transfer_required')}</p>
            ) : (
              <>
                <p>{t('teams.leave_explanation')}</p>
                <button
                  type="button"
                  className="btn btn--ghost"
                  disabled={busy}
                  onClick={() =>
                    ask(
                      t('teams.leave_confirm', { name: team.name }),
                      async () => {
                        await client.leave();
                        window.location.assign('/app');
                      },
                      false,
                    )
                  }
                >
                  {t('teams.leave')}
                </button>
              </>
            )}
          </details>
        </>
      )}
      {confirm && (
        <ActionConfirmation
          message={confirm.message}
          onAnswer={(accepted) => {
            const action = confirm.action;
            const refresh = confirm.refresh;
            setConfirm(null);
            if (accepted) void perform(action, refresh);
          }}
        />
      )}
    </section>
  );
}
