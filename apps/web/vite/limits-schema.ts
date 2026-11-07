// Shape of packages/contracts/limits.yaml as consumed by the web app. The key lists are the single
// source for both the runtime validation (vite/limits.ts) and the `Limits` type, so a renamed key in
// the YAML fails the build instead of silently becoming `undefined`.
export const LIMIT_GROUPS = {
  text: [
    'rules_text',
    'ui_customization_request',
    'saved_rule_title',
    'saved_rule_body',
    'rule_picker_search',
    'workflow_name',
    'project_name',
    'reprocess_reason',
    'display_name',
    'company_name',
    'email',
    'password_min',
    'password_max',
    'api_key_label',
    'coupon_code',
    'contact_message',
    'admin_note',
    'finding_feedback_note',
    'blog_comment',
  ],
  files: [
    'uploaded_file_name_bytes',
    'document_file_max_mb',
    'document_run_max_mb',
    'media_file_max_gb',
    'media_file_max_hours',
    'media_run_max_gb',
    'media_run_max_hours',
    'zip_max_entries',
    'zip_max_ratio',
    'zip_max_uncompressed_mb',
    'zip_max_depth',
  ],
  http: [
    'json_body_max_kb',
    'header_max_kb',
    'read_header_timeout_s',
    'request_timeout_s',
    'sse_idle_timeout_s',
  ],
  ids: [
    'idempotency_key_max',
    'api_key_scopes_max',
    'project_members_max',
    'project_rules_max',
    'account_members_max',
    'files_per_run_hard_max',
    'media_files_per_run_hard_max',
  ],
} as const;

export type LimitGroup = keyof typeof LIMIT_GROUPS;

export type Limits = {
  readonly version: number;
} & {
  readonly [G in LimitGroup]: { readonly [K in (typeof LIMIT_GROUPS)[G][number]]: number };
};

/**
 * Validates the parsed YAML. Every listed key must be a finite number >= 0 (0 means "disabled",
 * e.g. blog comments). Extra keys are kept so new limits flow through without a web change.
 */
export function validateLimits(data: unknown, source: string): Limits {
  if (data === null || typeof data !== 'object') {
    throw new Error(`${source}: conteúdo inválido (esperado um mapa YAML)`);
  }
  const root = data as Record<string, unknown>;
  const problems: string[] = [];
  if (typeof root['version'] !== 'number') problems.push('version (número)');
  for (const [group, keys] of Object.entries(LIMIT_GROUPS)) {
    const section = root[group];
    if (section === null || typeof section !== 'object') {
      problems.push(`${group} (mapa)`);
      continue;
    }
    for (const key of keys) {
      const value = (section as Record<string, unknown>)[key];
      if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) {
        problems.push(`${group}.${key} (número >= 0)`);
      }
    }
  }
  if (problems.length > 0) {
    throw new Error(`${source}: limites ausentes ou inválidos: ${problems.join(', ')}`);
  }
  return data as Limits;
}
