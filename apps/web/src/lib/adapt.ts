// Maps the API's run and findings onto the results screen. Evidence is grouped by source file:
// the first file goes to the left panel, the others to the right, each cited item as a row.
import type {
  Confidence,
  EvidenceRow,
  Finding,
  FindingStatus,
  MatchType,
  RunResults,
  TableEvidence,
} from '../components/results/types';
import type { EvidenceItem, FindingView, RunView } from './pipeline';

const DATE_KINDS = new Set([
  'early_payment',
  'paid_late_no_fee',
  'overdue_open',
  'ap_premature_recharge',
]);

function matchType(f: FindingView): MatchType {
  const fine = f.code.replace(/^recon\./, '');
  if (f.kind === 'duplicate') return 'duplicate';
  if (f.kind === 'missing') return 'missing';
  if (DATE_KINDS.has(fine)) return 'date';
  return 'amount';
}

/** finding.json `confidence_label`: Confirmado, Provável or Precisa de você. */
function confidence(label: string): Confidence {
  if (label === 'Confirmado') return 'high';
  if (label === 'Provável') return 'medium';
  return 'low';
}

/** API finding status → the screen's decision status. */
export function decisionStatus(status: string): FindingStatus {
  switch (status) {
    case 'confirmed':
      return 'accepted';
    case 'dismissed':
      return 'rejected';
    case 'resolved':
      return 'edited';
    default:
      return 'open';
  }
}

/** The screen's decision → the API feedback status. */
export function feedbackStatus(status: FindingStatus): string {
  switch (status) {
    case 'accepted':
      return 'confirmed';
    case 'rejected':
      return 'dismissed';
    case 'edited':
      return 'resolved';
    default:
      return 'open';
  }
}

function row(item: EvidenceItem, index: number): EvidenceRow {
  const v = item.values ?? {};
  const document = v.document ?? item.label;
  return {
    id: `${item.source_id}:${item.locator?.row ?? index}`,
    date: v.date ?? '',
    description: v.description ?? item.excerpt ?? item.label ?? '',
    amount: typeof v.amount === 'number' ? v.amount / 100 : 0,
    ...(document ? { document } : {}),
    cited: true,
  };
}

function table(name: string, items: EvidenceItem[]): TableEvidence {
  return { kind: 'table', sourceName: name, rows: items.map(row) };
}

export function toResults(run: RunView, findings: FindingView[]): RunResults {
  const names = new Map(run.sources.map((s) => [s.id, s.file_name]));
  const mapped: Finding[] = findings.map((f) => {
    const groups = new Map<string, EvidenceItem[]>();
    for (const e of f.evidence) {
      const list = groups.get(e.source_id) ?? [];
      list.push(e);
      groups.set(e.source_id, list);
    }
    const [first, ...rest] = [...groups.entries()];
    const left = table(first ? (names.get(first[0]) ?? first[0]) : '', first ? first[1] : []);
    const restItems = rest.flatMap(([, items]) => items);
    const rightName = rest.map(([id]) => names.get(id) ?? id).join(' · ');
    const right = table(rightName, restItems);
    return {
      id: f.id,
      severity: f.severity,
      documentRows: (run.summary?.reconciliation?.receivables ?? []).filter((row) =>
        f.evidence.some((e) => e.source_id === row.source_id && e.locator?.row === row.source_row),
      ),
      title: f.title,
      matchType: matchType(f),
      amountAtRisk: f.amount_at_risk / 100,
      currency: f.currency || 'BRL',
      confidence: confidence(f.confidence_label),
      status: decisionStatus(f.status),
      explanation: f.explanation,
      ...(f.feedback?.note ? { note: f.feedback.note } : {}),
      left,
      right,
    };
  });
  return {
    runId: run.id,
    fileCount: run.sources.length,
    findings: mapped,
    cleanMatches: [],
    ...(run.summary?.reconciliation ? { document: run.summary.reconciliation } : {}),
    sourceWarnings: run.summary?.source_warnings ?? [],
  };
}
