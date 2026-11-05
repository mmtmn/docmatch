package auth

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"

	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

var digitsRe = regexp.MustCompile(`^[0-9]{6}$`)

type emailStartRequest struct {
	Email string `json:"email"`
}

type emailStartResponse struct {
	ChallengeID  string    `json:"challenge_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	ResendAfterS int       `json:"resend_after_s"`
}

type emailVerifyRequest struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

// codeMAC keys the six-digit code to the challenge id, so a leaked table
// reveals neither codes nor which challenge a hash belongs to.
func (s *Service) codeMAC(id uuid.UUID, code string) []byte {
	return crypto.MAC(s.d.Keys.Key(crypto.PurposeEmailCode), id[:], []byte(code))
}

// emailStart handles POST /auth/email/start. The response and the work done
// are the same whether or not the address has an account: a challenge is
// always created and the e-mail always queued.
func (s *Service) emailStart(w http.ResponseWriter, r *http.Request) {
	if !s.d.EmailEnabled {
		apierr.Write(w, r, contracts.ErrServiceUnavailable)
		return
	}
	var req emailStartRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	addr, ok := s.normalizeEmail(req.Email)
	if !ok {
		apierr.Validation(w, r, apierr.Field{Path: "/email", Code: apierr.FieldInvalid, Max: s.d.Limits.Text.Email})
		return
	}
	if !s.allow(w, r, limitKey(s.d.ClientIP(r), addr)) {
		return
	}
	now := s.d.Now()
	id, err := uuid.NewV7()
	if err != nil {
		apierr.Internal(w, r, err)
		return
	}
	token := crypto.RandomToken(32)
	code := crypto.RandomDigits(codeDigits)
	ch := Challenge{
		ID: id, Email: addr, TokenHash: crypto.Digest(token), CodeHash: s.codeMAC(id, code), Purpose: challengePurpose,
		IPPrefix: session.IPPrefix(s.d.ClientIP(r)), CreatedAt: now, ExpiresAt: now.Add(ChallengeTTL),
	}
	public := publicid.Encode(publicid.PrefixChallenge, id)
	link := s.webURL(s.d.MagicLinkPath) + "?challenge_id=" + public + "&code=" + url.QueryEscape(token)
	if PlatformOf(r) != PlatformWeb {
		// Asked from an app: only the code. A link opened on the phone would sign in its browser
		// and spend the challenge the app is waiting for.
		link = ""
	}
	msg := email.LoginMessage(addr, email.LoginData{Code: code, Link: link, Expires: ch.ExpiresAt, Minutes: int(ChallengeTTL / time.Minute)})
	if err := s.d.Store.CreateChallenge(r.Context(), ch, msg); err != nil {
		apierr.Internal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, emailStartResponse{ChallengeID: public, ExpiresAt: ch.ExpiresAt.UTC(), ResendAfterS: int(ResendAfter / time.Second)})
}

// emailVerify handles POST /auth/email/verify with the typed code or the
// magic-link token.
func (s *Service) emailVerify(w http.ResponseWriter, r *http.Request) {
	var req emailVerifyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	id, err := publicid.Decode(publicid.PrefixChallenge, req.ChallengeID)
	if err != nil {
		apierr.Write(w, r, contracts.ErrEmailChallengeNotFound)
		return
	}
	isCode := digitsRe.MatchString(req.Code)
	isToken := crypto.ValidToken(req.Code, 32)
	if !isCode && !isToken {
		apierr.Validation(w, r, apierr.Field{Path: "/code", Code: apierr.FieldInvalid})
		return
	}
	if !s.allow(w, r, limitKey(s.d.ClientIP(r), req.ChallengeID)) {
		return
	}
	ch, ok, err := s.d.Store.ChallengeByID(r.Context(), id)
	if err != nil {
		apierr.Internal(w, r, err)
		return
	}
	now := s.d.Now()
	switch {
	case !ok || ch.ConsumedAt != nil || ch.Purpose != challengePurpose:
		apierr.Write(w, r, contracts.ErrEmailChallengeNotFound)
		return
	case !now.Before(ch.ExpiresAt):
		apierr.Write(w, r, contracts.ErrEmailCodeExpired)
		return
	case ch.Attempts >= ChallengeAttempts:
		w.Header().Set("Retry-After", strconv.Itoa(int(ch.ExpiresAt.Sub(now)/time.Second)+1))
		apierr.Write(w, r, contracts.ErrTooManyAttempts)
		return
	}
	// Spend one attempt atomically BEFORE comparing: a read-then-increment
	// would let parallel guesses all pass the check above.
	if _, claimed, err := s.d.Store.ClaimChallengeAttempt(r.Context(), id, ChallengeAttempts); err != nil {
		apierr.Internal(w, r, err)
		return
	} else if !claimed {
		w.Header().Set("Retry-After", strconv.Itoa(int(ch.ExpiresAt.Sub(now)/time.Second)+1))
		apierr.Write(w, r, contracts.ErrTooManyAttempts)
		return
	}
	// Both comparisons always run so the timing does not say which form was sent.
	codeOK := crypto.Equal(s.codeMAC(id, req.Code), ch.CodeHash)
	tokenOK := crypto.Equal(crypto.Digest(req.Code), ch.TokenHash)
	if (!isCode || !codeOK) && (!isToken || !tokenOK) {
		apierr.Write(w, r, contracts.ErrEmailCodeInvalid)
		return
	}
	consumed, ok, err := s.d.Store.ConsumeChallenge(r.Context(), id)
	if err != nil {
		apierr.Internal(w, r, err)
		return
	}
	if !ok {
		apierr.Write(w, r, contracts.ErrEmailChallengeNotFound)
		return
	}
	identity := Identity{Provider: ProviderEmail, Email: consumed.Email, EmailVerified: true}
	out, err := s.completeLogin(r.Context(), w, r, identity, nil, map[string]any{"challenge_id": req.ChallengeID, "via": via(isToken)})
	if err != nil {
		s.logRefused(r, identity, err)
		apierr.Write(w, r, codeFor(err))
		return
	}
	s.writeSession(w, out)
}

func via(token bool) string {
	if token {
		return "link"
	}
	return "code"
}
