// Island for /resultados while the API is not wired: feeds the screen with mocked data and
// resolves the demo audio to a blob: URL on the client (null during SSR and hydration, so the
// markup matches; useSyncExternalStore swaps in the client value right after).
import { useMemo, useSyncExternalStore } from 'react';
import { mockAudioUrl, mockResults } from '../../lib/mock/results';
import { ResultsScreen } from './ResultsScreen';
import type { RunResults } from './types';

function withMedia(results: RunResults, src: string | null): RunResults {
  return {
    ...results,
    findings: results.findings.map((finding) => ({
      ...finding,
      left: finding.left.kind === 'table' ? finding.left : { ...finding.left, src },
      right: finding.right.kind === 'table' ? finding.right : { ...finding.right, src },
    })),
  };
}

const subscribe = (): (() => void) => () => {};

export function ResultsDemo() {
  const audioUrl = useSyncExternalStore(subscribe, mockAudioUrl, () => null);
  const results = useMemo(() => withMedia(mockResults, audioUrl), [audioUrl]);
  return <ResultsScreen results={results} />;
}
