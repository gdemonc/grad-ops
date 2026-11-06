-- 0002_approval_binding.sql
--
-- Bind an approval to the exact contract it authorises, and make a token match
-- at most one approval.
--
-- Without this column an approval would be a comment rather than a capability:
-- the approver saw one action, but the executor could run another with the same
-- approval id. The digest is stored next to the decision so consumption can be
-- a single atomic statement that also checks the binding.

ALTER TABLE approvals ADD COLUMN contract_digest TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_approvals_token_hash
    ON approvals (token_hash)
    WHERE token_hash IS NOT NULL;
