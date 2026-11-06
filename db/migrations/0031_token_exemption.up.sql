-- Private test organizations can run without purchasing credits. The flag is
-- operator-managed; no account/profile API accepts it. Keep zero-value holds
-- so retries, settlement and the append-only ledger retain their normal flow.
ALTER TABLE credit_accounts ADD COLUMN token_exempt boolean NOT NULL DEFAULT false;
ALTER TABLE credit_holds ADD COLUMN sponsored boolean NOT NULL DEFAULT false;
ALTER TABLE credit_holds DROP CONSTRAINT credit_holds_amount_check;
ALTER TABLE credit_holds ADD CONSTRAINT credit_holds_amount_check
  CHECK ((NOT sponsored AND amount > 0) OR (sponsored AND amount = 0));
