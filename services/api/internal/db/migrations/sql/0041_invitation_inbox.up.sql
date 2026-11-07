-- Verified recipients can find pending invitations without visiting an e-mail link.
CREATE INDEX team_invitations_inbox ON team_invitations(email, id) WHERE status='pending';
