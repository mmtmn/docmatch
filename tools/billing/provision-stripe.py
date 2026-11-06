#!/usr/bin/env python3
"""Provision Docmatch's Stripe catalog from pricing.yaml; write IDs/secrets to an ignored env file."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import urllib.error
import urllib.parse
import urllib.request
from decimal import Decimal

import yaml

API = 'https://api.stripe.com/v1/'
VERSION = '2026-10-28.endive'
EVENTS = ['checkout.session.completed', 'checkout.session.async_payment_succeeded',
          'checkout.session.async_payment_failed', 'invoice.paid', 'invoice.payment_failed',
          'customer.subscription.created', 'customer.subscription.updated',
          'customer.subscription.deleted', 'charge.refunded', 'charge.dispute.created']


def read_env(path):
    values = {}
    if path.exists():
        for line in path.read_text().splitlines():
            if '=' in line and not line.lstrip().startswith('#'):
                key, value = line.split('=', 1)
                values[key.strip()] = ' '.join(shlex.split(value, comments=True))
    return values


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', type=Path, default=Path('.env.providers'))
    parser.add_argument('--pricing', type=Path, default=Path('config/pricing.yaml'))
    parser.add_argument('--base-url', required=True)
    parser.add_argument('--out', type=Path, default=Path('.env.stripe'))
    parser.add_argument('--allow-live', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    values = {**read_env(args.env_file), **os.environ}
    key = values.get('STRIPE_SECRET_KEY', '')
    if not key.startswith(('sk_test_', 'sk_live_', 'rk_test_', 'rk_live_')):
        raise SystemExit('Set STRIPE_SECRET_KEY in the environment or ignored env file.')
    live = '_live_' in key
    if live and not args.allow_live:
        raise SystemExit('Live provisioning requires --allow-live.')
    base = args.base_url.rstrip('/')
    if urllib.parse.urlparse(base).scheme != 'https':
        raise SystemExit('--base-url must use HTTPS.')
    pricing = yaml.safe_load(args.pricing.read_text())
    saved = read_env(args.out)
    saved['STRIPE_SECRET_KEY'] = key
    if values.get('STRIPE_PUBLISHABLE_KEY'):
        saved['STRIPE_PUBLISHABLE_KEY'] = values['STRIPE_PUBLISHABLE_KEY']

    def persist():
        fd = os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, 'w') as f:
            f.write('# Private Stripe configuration. Never commit.\n')
            for k, v in sorted(saved.items()):
                f.write(k + '=' + v + '\n')
        args.out.chmod(0o600)

    def api(path, params=None):
        encoded = urllib.parse.urlencode(params or {}, doseq=True).encode()
        headers = {'Authorization': 'Bearer ' + key, 'Stripe-Version': VERSION}
        if params is not None:
            headers['Idempotency-Key'] = 'docmatch-setup-' + hashlib.sha256(path.encode() + encoded).hexdigest()
        req = urllib.request.Request(API + path, data=encoded if params is not None else None, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            detail = json.load(error).get('error', {})
            raise SystemExit('Stripe HTTP %s (%s): %s' % (error.code, detail.get('code', detail.get('type')), detail.get('message', 'request failed'))) from None

    def all_items(path):
        result, cursor = [], ''
        while True:
            query = {'limit': 100}
            if cursor:
                query['starting_after'] = cursor
            page = api(path + '?' + urllib.parse.urlencode(query))
            result.extend(page['data'])
            if not page.get('has_more'):
                return result
            cursor = page['data'][-1]['id']

    products = all_items('products')
    prices = all_items('prices')
    catalog = {code: {'name': 'docmatch ' + {'starter': 'Essencial', 'pro': 'Plus', 'business': 'Max'}[code], 'tokens': p['tokens'],
                      'cents': int(Decimal(str(p['price_brl'])) * 100), 'recurring': True}
               for code, p in pricing['plans'].items()}
    catalog['topup'] = {'name': 'Tokens docmatch', 'tokens': 1,
                        'cents': int(Decimal(str(pricing['token_brl'])) * 100), 'recurring': False}
    for code, plan in catalog.items():
        matches = [p for p in products if p.get('metadata', {}).get('app') == 'docmatch' and p['metadata'].get('plan') == code]
        if len(matches) > 1:
            raise SystemExit('Ambiguous Docmatch product for ' + code)
        product = matches[0] if matches else api('products', {
            'name': plan['name'], 'metadata[app]': 'docmatch', 'metadata[plan]': code,
            'metadata[tokens]': plan['tokens'], 'url': base,
            'images[]': base + '/brand/docmatch-avatar-512.png',
            'description': str(plan['tokens']) + (' tokens por mês.' if plan['recurring'] else ' token de recarga; validade de 12 meses.')})
        if product['name'] != plan['name']:
            api('products/' + product['id'], {'name': plan['name']})
        matches = [p for p in prices if p.get('active') and p['product'] == product['id']
                   and p['currency'] == 'brl' and p['unit_amount'] == plan['cents']
                   and bool(p.get('recurring')) == plan['recurring']
                   and (not plan['recurring'] or p['recurring']['interval'] == 'month')]
        if len(matches) > 1:
            raise SystemExit('Ambiguous Docmatch price for ' + code)
        params = {'product': product['id'], 'currency': 'brl', 'unit_amount': plan['cents'],
                  'metadata[app]': 'docmatch', 'metadata[plan]': code, 'metadata[tokens]': plan['tokens'],
                  'lookup_key': 'docmatch_brl_' + code + ('_monthly_' if plan['recurring'] else '_unit_') + str(plan['cents']) + '_' + str(plan['tokens'])}
        if plan['recurring']:
            params['recurring[interval]'] = 'month'
        price = matches[0] if matches else api('prices', params)
        if product.get('default_price') != price['id']:
            api('products/' + product['id'], {'default_price': price['id']})
        saved['DM_STRIPE_PRICE_' + code.upper()] = price['id']
        persist()
        print(code + ': product and price ready (' + ('monthly' if plan['recurring'] else 'one-time') + ')')

    methods = [p for p in all_items('payment_method_configurations') if p.get('name') == 'docmatch' and p.get('active')]
    if len(methods) > 1:
        raise SystemExit('Ambiguous Docmatch payment method configuration')
    methods = methods[0] if methods else api('payment_method_configurations', {
        'name': 'docmatch', 'card[display_preference][preference]': 'on'})
    updates = {}
    for method, setting in methods.items():
        if isinstance(setting, dict) and 'display_preference' in setting:
            desired = 'on' if method in ('card', 'apple_pay', 'google_pay') else 'off'
            if setting['display_preference']['preference'] != desired:
                updates[method + '[display_preference][preference]'] = desired
    if updates:
        api('payment_method_configurations/' + methods['id'], updates)
    saved['DM_STRIPE_PAYMENT_METHOD_CONFIGURATION_ID'] = methods['id']
    persist()
    print('Dedicated card payment method configuration ready')

    portals = [p for p in all_items('billing_portal/configurations') if p.get('metadata', {}).get('app') == 'docmatch' and p.get('active')]
    if len(portals) > 1:
        raise SystemExit('Ambiguous Docmatch portal configuration')
    portal = portals[0] if portals else api('billing_portal/configurations', {
        'name': 'docmatch', 'metadata[app]': 'docmatch', 'default_return_url': base + '/conta',
        'business_profile[headline]': 'Gerencie sua assinatura docmatch',
        'business_profile[privacy_policy_url]': base + '/privacidade',
        'business_profile[terms_of_service_url]': base + '/termos',
        'features[customer_update][enabled]': 'true',
        'features[customer_update][allowed_updates][]': ['name', 'email', 'address', 'tax_id'],
        'features[invoice_history][enabled]': 'true', 'features[payment_method_update][enabled]': 'true',
        'features[subscription_cancel][enabled]': 'true', 'features[subscription_cancel][mode]': 'at_period_end',
        'features[subscription_update][enabled]': 'false'})
    saved['DM_STRIPE_PORTAL_CONFIGURATION_ID'] = portal['id']
    persist()
    print('Dedicated customer portal ready')

    endpoint_url = base + '/v1/webhooks/stripe'
    endpoints = [e for e in all_items('webhook_endpoints') if e['url'] == endpoint_url]
    if len(endpoints) > 1:
        raise SystemExit('Ambiguous webhook endpoints')
    if endpoints:
        endpoint = endpoints[0]
        if not saved.get('STRIPE_WEBHOOK_SECRET'):
            raise SystemExit('Existing endpoint secret missing locally; recover it from SSM or Stripe Dashboard.')
    else:
        endpoint = api('webhook_endpoints', {'url': endpoint_url, 'api_version': VERSION,
                       'enabled_events[]': EVENTS, 'metadata[app]': 'docmatch',
                       'description': 'docmatch billing: signed, idempotent payment processing'})
        saved['STRIPE_WEBHOOK_SECRET'] = endpoint['secret']
        saved['DM_STRIPE_WEBHOOK_ENDPOINT_ID'] = endpoint['id']
        persist()
        api('webhook_endpoints/' + endpoint['id'], {'disabled': 'true'})
    print('Webhook ready; existing status preserved. Enable new endpoints after verifying the handler.')
    print('Mode:', 'LIVE' if live else 'TEST')
    print('Configuration saved to', args.out)

if __name__ == '__main__':
    main()
