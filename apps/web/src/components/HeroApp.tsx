// Wires the HeroForm to its providers. Until the API client lands (plan phase: API), the
// providers are demo stubs enabled by PUBLIC_DM_FEATURE_RESULTS so the whole flow can be
// exercised end to end (quote → run → results screen) with mocked data.
import { featureResults } from '../lib/env';
import { mockQuoteProvider, mockRuleOptions } from '../lib/mock/quote';
import { HeroForm } from './HeroForm';

const RESULTS_PATH = '/resultados';

function demoSubmit(): Promise<void> {
  // TODO(plan phase: API): POST /v1/runs with the quote id, then navigate to the run's page.
  window.location.assign(RESULTS_PATH);
  return Promise.resolve();
}

export function HeroApp() {
  if (!featureResults) return <HeroForm />;
  return (
    <HeroForm
      ruleOptions={mockRuleOptions}
      quoteProvider={mockQuoteProvider}
      onSubmit={demoSubmit}
    />
  );
}
