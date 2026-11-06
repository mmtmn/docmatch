#!/usr/bin/env python3
"""Score a real API result against a golden answer key. Never send the key to a model.

The corrected-document policy keeps proposals/withholding pending. Expected
balances are derived from the golden allocations, not copied from the API.
Reports contain aggregate counts and hashes, never customer rows or amounts.
"""
import argparse
import collections
import hashlib
import json
from decimal import Decimal
from pathlib import Path
import yaml

def cents(value):
    return int((Decimal(str(value or 0)) * 100).quantize(Decimal('1')))

def verify(expected, run, findings):
    errors = []
    doc = run.get('summary', {}).get('reconciliation', {})
    titles = expected['titles']
    rows = doc.get('receivables', [])
    actual = {(r['source_name'], r['reference']): r for r in rows}
    by_id = {t['id']: (expected['sources'][t['source']], str(t['ref'])) for t in titles}
    actual_ids = {r['id']: (r['source_name'], r['reference']) for r in rows}
    reverse = {v:k for k,v in by_id.items()}
    source_names = {s['id']: s['file_name'] for s in run['sources']}
    expected_rows = {}
    source_counts = collections.Counter()
    expected_totals = dict.fromkeys(['original_amount','duplicate_excluded','received_confirmed','principal_confirmed','open_balance','proposed_amount','projected_open_balance','unapplied_cash','bank_fees','fines_received','withholding_pending'],0)
    title_ok = 0
    for t in titles:
        key=by_id[t['id']];a=actual.get(key,{})
        source_counts[t['source']]+=1
        expected_rows[(key[0],source_counts[t['source']])]='title:'+t['id']
        face,paid=cents(t['amount']),cents(t.get('paid_amount'))
        flags=set(t.get('flags',[]));duplicate=t.get('duplicate_of','')
        proposed=min(face,paid) if 'proposed' in flags else 0
        received=0 if proposed or duplicate else paid
        applied=min(received,face)
        extra=max(0,received-face)
        tax=face-received if 'withholding_pending' in flags else 0
        opened=0 if duplicate else max(0,face-applied)
        status='duplicate' if duplicate else 'review' if proposed else 'withholding' if tax else 'paid' if opened==0 else 'partial' if applied else 'open'
        expected_title={'reference':str(t['ref']),'customer':t['customer'],'tax_id':str(t['tax_id']), 'source_row':source_counts[t['source']], 'due_date':str(t['due']), 'paid_on':str(t['paid_on']) if t.get('paid_on') and not proposed else '', 'original_amount':face,'received_amount':received,'applied_principal':applied,'open_amount':opened,'proposed_amount':proposed,'fine_received':extra,'withholding_pending':tax,'status':status}
        checks=[a.get(k)==v for k,v in expected_title.items()]
        checks += [set(a.get('flags',[]))==flags, actual_ids.get(a.get('duplicate_of'),a.get('duplicate_of',''))==by_id.get(duplicate,duplicate)]
        expected_bank=[l['n'] for l in expected['bank_lines'] if t['id'] in l.get('matched',[]) and not duplicate]
        checks.append(sorted(a.get('bank_rows',[]))==sorted(expected_bank))
        if all(checks):title_ok+=1
        else:errors.append('title '+t['id']+': '+','.join(k for k,v in expected_title.items() if a.get(k)!=v)+' flags/links if applicable')
        for k,v in {'original_amount':face,'duplicate_excluded':face if duplicate else 0,'received_confirmed':received,'principal_confirmed':applied,'open_balance':opened,'proposed_amount':proposed,'fines_received':extra,'withholding_pending':tax}.items():expected_totals[k]+=v
    bank_ok=0
    actual_bank={l['row']:l for l in doc.get('bank_entries',[])}
    for e in expected['bank_lines']:
        a=actual_bank.get(e['n'],{})
        em={by_id.get(v,v) for v in e.get('matched',[])};am={actual_ids.get(v,v) for v in a.get('matched_ids',[])}
        checks=[a.get('kind')==e['kind'],a.get('rule')==e['rule'],a.get('proposed')==e.get('proposed',False),em==am,a.get('amount')==cents(e['amount'])*(-1 if e['side']=='D' else 1),a.get('date')==str(e['date']),a.get('source_name')==expected['sources']['bank']]
        if all(checks):bank_ok+=1
        else:errors.append('bank '+str(e['n']))
        if e['kind']=='fee':expected_totals['bank_fees']+=cents(e['amount'])
        if e['side']=='C' and (e['kind']=='unapplied' or e.get('proposed')):expected_totals['unapplied_cash']+=cents(e['amount'])
    expected_totals['projected_open_balance']=expected_totals['open_balance']-expected_totals['proposed_amount']
    def evidence_key(e):
        file=source_names.get(e.get('source_id'));row=e.get('locator',{}).get('row');label=e.get('label','')
        if file==expected['sources']['bank']:return 'bank:'+str(row)
        if (file,row) in expected_rows:return expected_rows[(file,row)]
        if file==expected['sources']['ap_inbox']:
            if label.startswith('E-mail ') and row == 1:return 'email:'+label.removeprefix('E-mail ')
            date=e.get('values',{}).get('date','')
            if date:return 'chat:'+date
        return 'unknown'
    finding_ok=0;used=set()
    for e in expected['findings']:
        candidates=[(i,f) for i,f in enumerate(findings) if f.get('code')=='recon.'+e['kind']]
        if len(candidates)==1:
            i,a=candidates[0]
            if a.get('severity')==e['severity'] and abs(a.get('amount_at_risk',-999)-cents(e['amount_at_risk_brl']))<=1 and {evidence_key(x) for x in a.get('evidence',[])}==set(e['evidence']):
                finding_ok+=1;used.add(i);continue
        errors.append('finding '+e['code'])
    total_ok=sum(doc.get('totals',{}).get(k)==v for k,v in expected_totals.items())
    if total_ok!=len(expected_totals):errors.append('totals')
    sections={'bank':{'ok':bank_ok,'total':len(expected['bank_lines'])},'titles':{'ok':title_ok,'total':len(titles)},'findings':{'ok':finding_ok,'total':len(expected['findings'])},'totals':{'ok':total_ok,'total':len(expected_totals)}}
    extras={'bank':max(0,len(doc.get('bank_entries',[]))-len(expected['bank_lines'])),'titles':max(0,len(rows)-len(titles)),'findings':len(findings)-len(used)}
    passed=run.get('status')=='completed' and all(s['ok']==s['total'] for s in sections.values()) and not any(extras.values())
    return {'passed':passed,'score_percent':100 if passed else round(sum(s['ok'] for s in sections.values())/sum(s['total'] for s in sections.values())*100,2),'sections':sections,'extras':extras,'differences':errors,'model_advice_present':all('Próximo passo sugerido:' in f.get('explanation','') for f in findings)}

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--expected',type=Path,required=True);parser.add_argument('--result',type=Path,required=True);parser.add_argument('--report',type=Path,required=True);args=parser.parse_args()
    expected=yaml.safe_load(args.expected.read_text());result=json.loads(args.result.read_text());report=verify(expected,result['run'],result['findings'])
    report.update({'run_id':result['run']['id'],'expected_sha256':hashlib.sha256(args.expected.read_bytes()).hexdigest(),'result_sha256':hashlib.sha256(args.result.read_bytes()).hexdigest()})
    args.report.parent.mkdir(parents=True,exist_ok=True);args.report.write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report,indent=2));raise SystemExit(0 if report['passed'] else 1)
if __name__=='__main__':main()
