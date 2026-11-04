// Island for /resultados while the API is not wired: feeds the screen with mocked data and
// resolves the demo audio to a blob: URL after hydration (the SSR markup has no src, so the
// client's first render matches it; the URL is applied from a callback, never during render).
import { useEffect, useMemo, useState } from 'react';
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

export function ResultsDemo() {
  const [audioUrl, setAudioUrl] = useState<string | null>(null);
  useEffect(() => {
    const frame = requestAnimationFrame(() => setAudioUrl(mockAudioUrl()));
    return () => cancelAnimationFrame(frame);
  }, []);
  const results = useMemo(() => withMedia(mockResults, audioUrl), [audioUrl]);
  return <ResultsScreen results={results} />;
}
