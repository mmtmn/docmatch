// Shapes of the results screen. They mirror what the API will return for a run (plan phase: API);
// until then src/lib/mock/results.ts feeds them.
export type Confidence = 'high' | 'medium' | 'low';
export type FindingStatus = 'open' | 'accepted' | 'rejected' | 'edited';
export type MatchType = 'missing' | 'amount' | 'date' | 'duplicate';

export interface EvidenceRow {
  id: string;
  date: string;
  description: string;
  amount: number;
  document?: string;
  /** Row cited by the finding; rendered highlighted. */
  cited?: boolean;
}

export interface TableEvidence {
  kind: 'table';
  sourceName: string;
  rows: EvidenceRow[];
}

export interface MediaEvidence {
  kind: 'audio' | 'video';
  sourceName: string;
  /** Resolved on the client (blob: or the media origin). */
  src: string | null;
  mimeType: string;
  /** Seconds into the media where the cited passage starts. */
  timestamp: number;
  transcript?: string;
}

export type Evidence = TableEvidence | MediaEvidence;

export interface Finding {
  id: string;
  title: string;
  matchType: MatchType;
  amountAtRisk: number;
  currency: string;
  confidence: Confidence;
  status: FindingStatus;
  explanation: string;
  rule?: string;
  left: Evidence;
  right: Evidence;
  note?: string;
}

export interface CleanMatch {
  id: string;
  date: string;
  description: string;
  amount: number;
  currency: string;
  leftSource: string;
  rightSource: string;
}

export interface RunResults {
  runId: string;
  fileCount: number;
  findings: Finding[];
  cleanMatches: CleanMatch[];
  /** Per file, in pt-BR, what did not take part in the reconciliation. */
  sourceWarnings?: string[];
  /** True while the data is mocked. */
  demo?: boolean;
}

export type ExportFormat = 'csv' | 'xlsx' | 'pdf';
