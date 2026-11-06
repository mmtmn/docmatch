-- Refuse rollback while sponsored holds exist, rather than rewriting history.
ALTER TABLE credit_holds DROP CONSTRAINT credit_holds_amount_check;
ALTER TABLE credit_holds ADD CONSTRAINT credit_holds_amount_check CHECK (amount > 0);
ALTER TABLE credit_holds DROP COLUMN sponsored;
ALTER TABLE credit_accounts DROP COLUMN token_exempt;
