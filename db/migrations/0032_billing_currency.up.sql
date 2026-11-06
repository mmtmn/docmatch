-- Existing order snapshots were priced in USD. New checkouts set BRL explicitly.
ALTER TABLE billing_orders ADD COLUMN currency text NOT NULL DEFAULT 'usd' CHECK (currency IN ('usd','brl'));
