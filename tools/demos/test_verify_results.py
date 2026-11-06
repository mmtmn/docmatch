"""Mutation checks use a real captured public-twin result supplied by the caller."""
import copy,json,sys
from pathlib import Path
import yaml
from verify_results import verify
expected=yaml.safe_load(Path(sys.argv[1]).read_text());result=json.loads(Path(sys.argv[2]).read_text())
assert verify(expected,result['run'],result['findings'])['passed']
for mutate in [lambda r:r['run']['summary']['reconciliation']['receivables'][0].update(open_amount=1),lambda r:r['run']['summary']['reconciliation']['bank_entries'][0].update(rule='wrong'),lambda r:r['findings'][0].update(evidence=[]),lambda r:r['findings'].append(copy.deepcopy(r['findings'][0])),lambda r:r['run']['summary']['reconciliation']['totals'].update(bank_fees=1)]:
 changed=copy.deepcopy(result);mutate(changed);assert not verify(expected,changed['run'],changed['findings'])['passed']
print('Five corruptions rejected: balance, matching rule, evidence, duplicate finding and totals.')
