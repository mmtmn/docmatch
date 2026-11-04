// Lighthouse CI (`pnpm lhci`): not part of `make ci`. Lighthouse 12+ dropped the budget audits, so
// the thresholds in lighthouse-budgets.json are translated into assertions here; the JSON stays
// the single place to edit them. Resource budgets are in KB of transfer size.
const budgets = require('./lighthouse-budgets.json');

const TIMING_AUDIT = {
  'largest-contentful-paint': 'largest-contentful-paint',
  'cumulative-layout-shift': 'cumulative-layout-shift',
  interactive: 'interactive',
  'total-blocking-time': 'total-blocking-time',
  'first-contentful-paint': 'first-contentful-paint',
  'speed-index': 'speed-index',
};

const assertions = {
  'categories:performance': ['error', { minScore: 0.9 }],
  'categories:accessibility': ['error', { minScore: 1 }],
  'categories:best-practices': ['warn', { minScore: 0.9 }],
  'categories:seo': ['warn', { minScore: 0.9 }],
};

for (const budget of budgets) {
  for (const timing of budget.timings ?? []) {
    const audit = TIMING_AUDIT[timing.metric];
    if (audit) assertions[audit] = ['error', { maxNumericValue: timing.budget }];
  }
  for (const size of budget.resourceSizes ?? []) {
    assertions[`resource-summary:${size.resourceType}:size`] = [
      'error',
      { maxNumericValue: size.budget * 1024 },
    ];
  }
  for (const count of budget.resourceCounts ?? []) {
    assertions[`resource-summary:${count.resourceType}:count`] = [
      'error',
      { maxNumericValue: count.budget },
    ];
  }
}

module.exports = {
  ci: {
    collect: {
      staticDistDir: './dist',
      url: ['/index.html', '/app.html', '/termos.html', '/privacidade.html'],
      numberOfRuns: 3,
      settings: {
        locale: 'pt-BR',
        onlyCategories: ['performance', 'accessibility', 'best-practices', 'seo'],
      },
    },
    assert: { assertions },
    upload: {
      target: 'filesystem',
      outputDir: './.lighthouseci',
    },
  },
};
