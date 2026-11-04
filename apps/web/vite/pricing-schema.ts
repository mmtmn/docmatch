// Public subset of config/pricing.yaml as consumed by the plans table and the demo quote. Only
// these fields are validated and exported: `virtual:pricing` ends up in client bundles, so the
// internal rates (margin, taxes, fees) and cost estimates in the same file are stripped here.
export const PLAN_IDS = ['starter', 'pro', 'business'] as const;
export type PlanId = (typeof PLAN_IDS)[number];

export const LANE_IDS = ['app', 'api_batch', 'api_standard', 'api_priority'] as const;
export type LaneId = (typeof LANE_IDS)[number];

export const PLAN_NUMBER_FIELDS = [
  'price_usd',
  'tokens',
  'document_files_per_run',
  'document_file_mb',
  'document_run_mb',
  'media_files_per_run',
  'media_run_gb',
  'media_run_hours',
  'concurrent_runs',
  'api_keys',
  'ui_customizations_per_month',
  'retention_days',
] as const;

export const PLAN_BOOLEAN_FIELDS = ['auto_rule_learning', 'escalation_model'] as const;

export type Plan = { readonly [K in (typeof PLAN_NUMBER_FIELDS)[number]]: number } & {
  readonly [K in (typeof PLAN_BOOLEAN_FIELDS)[number]]: boolean;
} & {
  readonly api_lanes: readonly LaneId[];
};

export interface Pricing {
  readonly token_usd: number;
  readonly min_tokens_per_run: number;
  readonly quote_ttl_minutes: number;
  readonly lanes: { readonly [K in LaneId]: number };
  readonly plans: { readonly [K in PlanId]: Plan };
  readonly rollover: {
    readonly plan_tokens_cycles: number;
    readonly topup_tokens_months: number;
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

export function validatePricing(data: unknown, source: string): Pricing {
  if (!isRecord(data)) throw new Error(`${source}: conteúdo inválido (esperado um mapa YAML)`);
  const problems: string[] = [];
  for (const key of ['token_usd', 'min_tokens_per_run', 'quote_ttl_minutes'] as const) {
    if (typeof data[key] !== 'number') problems.push(`${key} (número)`);
  }
  const lanes = data['lanes'];
  if (!isRecord(lanes)) problems.push('lanes (mapa)');
  else {
    for (const lane of LANE_IDS) {
      if (typeof lanes[lane] !== 'number') problems.push(`lanes.${lane} (número)`);
    }
  }
  const rollover = data['rollover'];
  if (
    !isRecord(rollover) ||
    typeof rollover['plan_tokens_cycles'] !== 'number' ||
    typeof rollover['topup_tokens_months'] !== 'number'
  ) {
    problems.push('rollover.plan_tokens_cycles e rollover.topup_tokens_months (números)');
  }
  const plans = data['plans'];
  if (!isRecord(plans)) problems.push('plans (mapa)');
  else {
    for (const id of PLAN_IDS) {
      const plan = plans[id];
      if (!isRecord(plan)) {
        problems.push(`plans.${id} (mapa)`);
        continue;
      }
      for (const field of PLAN_NUMBER_FIELDS) {
        if (typeof plan[field] !== 'number') problems.push(`plans.${id}.${field} (número)`);
      }
      for (const field of PLAN_BOOLEAN_FIELDS) {
        if (typeof plan[field] !== 'boolean') problems.push(`plans.${id}.${field} (booleano)`);
      }
      const apiLanes = plan['api_lanes'];
      if (
        !Array.isArray(apiLanes) ||
        !apiLanes.every((lane) => (LANE_IDS as readonly string[]).includes(String(lane)))
      ) {
        problems.push(`plans.${id}.api_lanes (lista com ${LANE_IDS.join(', ')})`);
      }
    }
  }
  if (problems.length > 0) {
    throw new Error(`${source}: preços ausentes ou inválidos: ${problems.join(', ')}`);
  }
  return publicSubset(data as unknown as Pricing);
}

/** Copies only the public fields, so unknown keys in the YAML never reach the browser. */
function publicSubset(data: Pricing): Pricing {
  const plans = {} as { [K in PlanId]: Plan };
  for (const id of PLAN_IDS) {
    const plan = data.plans[id];
    // Built field by field from the validated lists above, so the final cast is sound.
    const copy: Record<string, unknown> = { api_lanes: [...plan.api_lanes] };
    for (const field of PLAN_NUMBER_FIELDS) copy[field] = plan[field];
    for (const field of PLAN_BOOLEAN_FIELDS) copy[field] = plan[field];
    plans[id] = copy as unknown as Plan;
  }
  const lanes = {} as { [K in LaneId]: number };
  for (const lane of LANE_IDS) lanes[lane] = data.lanes[lane];
  return {
    token_usd: data.token_usd,
    min_tokens_per_run: data.min_tokens_per_run,
    quote_ttl_minutes: data.quote_ttl_minutes,
    lanes,
    plans,
    rollover: {
      plan_tokens_cycles: data.rollover.plan_tokens_cycles,
      topup_tokens_months: data.rollover.topup_tokens_months,
    },
  };
}
