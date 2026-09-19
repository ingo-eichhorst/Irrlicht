package main

// The desktop enrollment HTTP surface (#1963): a one-time code exchanged
// for an ordinary bearer token, the desktop counterpart to phone pairing
// (push_handlers.go). Phase 1 (this file): the unauthenticated redeem route
// and the auth-off guard — enrollment mints an ordinary token, so it
// presupposes a token store exactly as push does (docs/mobile-notifications-arc42.md
// §8.1), but independently of push: a relay with push disabled must still
// be able to enroll a desktop. Phase 2 (below): the authenticated mint
// route, POST /api/v1/enroll/requests, for an operator who already holds a
// token — the counterpart to the unauthenticated `enroll new` CLI path.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"irrlicht/core/pkg/onetimecode"
)

// enrollAuthRequired names the fix wherever an enroll route is refused on
// an anonymous relay: there is no token store to mint into.
const enrollAuthRequired = "enrollment requires --auth tokens-file (there is no token store to mint an enrolled desktop's token into)"

// enrollBodyLimit bounds a redeem request body — pre-authentication (the
// code is the credential), and a {"code":"XXXX-XXXX"} body is under 32
// bytes, so anything near the limit is garbage, not a client.
const enrollBodyLimit = 1 << 10

// enrollCodeInvalidMsg is the single uniform redeem failure the wire ever
// sees: unknown, expired and already-used codes answer identically, so the
// endpoint is no oracle for which codes exist. Mirrors pairCodeInvalidMsg.
const enrollCodeInvalidMsg = "invalid or expired enrollment code"

// enrollDefaultLabel names an enrolled desktop's token when neither the
// redeeming desktop nor the code it redeemed carried a label.
const enrollDefaultLabel = "desktop"

// registerEnrollRoutes wires the enrollment endpoints onto mux. mgr is nil
// exactly when auth is off (buildEnrollManager returns nil then, mirroring
// buildPushService) — every enroll route is a 403 naming the fix, the same
// shape registerPushRoutes uses for push, but gated on the token store
// alone: enrollment does not depend on push being enabled.
func registerEnrollRoutes(mux *http.ServeMux, mgr *onetimecode.Manager, store *authStore, handoff pairingHandoff) {
	if mgr == nil || store == nil {
		mux.HandleFunc("POST /api/v1/enroll/redeem", handleEnrollDisabled)
		mux.HandleFunc("POST /api/v1/enroll/requests", handleEnrollDisabled)
		return
	}
	mux.HandleFunc("POST /api/v1/enroll/redeem", handleEnrollRedeem(mgr, store))
	mux.HandleFunc("POST /api/v1/enroll/requests", requireToken(store, handleMintEnroll(mgr, handoff)))
}

// handleMintEnroll mints an enrollment code in the caller's workspace — the
// authenticated counterpart to `enroll new`, for an operator who already
// holds a token (Phase 2, #1963). Mirrors handleMintPairing
// (push_handlers.go): the bearer gate has already validated the client
// token via requireToken, no request body, workspace read from the token's
// identity. This route mints with no label; handleEnrollRedeem lets the
// desktop that redeems the code name itself instead (the label a phone
// supplies at pairing time, via pairRequest.Label, is the same shape) —
// see that function for the fallback order.
func handleMintEnroll(mgr *onetimecode.Manager, handoff pairingHandoff) http.HandlerFunc {
	type mintResp struct {
		Code            string `json:"code"`
		ExpiresIn       int    `json:"expires_in"`
		EnrollURL       string `json:"enroll_url,omitempty"`
		EnrollURLReason string `json:"enroll_url_reason,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		code, ttl, err := mgr.Mint(workspaceOf(r), "")
		if errors.Is(err, onetimecode.ErrTooManyCodes) {
			pushError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if err != nil {
			pushError(w, http.StatusInternalServerError, "minting enrollment code failed")
			return
		}
		pushJSON(w, http.StatusCreated, mintResp{
			Code: code, ExpiresIn: int(ttl / time.Second),
			EnrollURL: handoff.enrollURL(code), EnrollURLReason: handoff.unavailableReason,
		})
	}
}

// handleEnrollDisabled refuses an enroll route on an anonymous relay,
// naming the fix.
func handleEnrollDisabled(w http.ResponseWriter, _ *http.Request) {
	pushError(w, http.StatusForbidden, enrollAuthRequired)
}

// enrollRedeemRequest is the body of POST /api/v1/enroll/redeem. The route
// carries no bearer gate: the one-time code IS the credential, exactly as
// pairRequest is for phone pairing. Label lets the desktop name itself at
// redeem time, exactly as a phone does via pairRequest.Label.
type enrollRedeemRequest struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// handleEnrollRedeem redeems an enrollment code for an ordinary device
// token — an ordinary TokenRecord in the code's workspace, so `token list`
// shows it and `token revoke` is the full revocation story, exactly as
// handlePair does for a paired phone (push_handlers.go). The label prefers,
// in order: what the redeeming desktop names itself in the request body,
// then whatever the code was minted with (`enroll new --label`; the
// authenticated mint route sets none), then enrollDefaultLabel. The auth
// store is reloaded synchronously after issuing, so the desktop can use the
// token on its very next request. Every redemption failure is one uniform
// 401 (no oracle); a saturated failure window is a 429.
func handleEnrollRedeem(mgr *onetimecode.Manager, store *authStore) http.HandlerFunc {
	type redeemResp struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req enrollRedeemRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, enrollBodyLimit)).Decode(&req); err != nil {
			pushError(w, http.StatusUnauthorized, enrollCodeInvalidMsg)
			return
		}
		workspace, mintLabel, err := mgr.Redeem(req.Code)
		if errors.Is(err, onetimecode.ErrRateLimited) {
			pushError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if err != nil {
			pushError(w, http.StatusUnauthorized, enrollCodeInvalidMsg)
			return
		}
		label := sanitizeLabel(req.Label)
		if label == "" {
			label = sanitizeLabel(mintLabel)
		}
		if label == "" {
			label = enrollDefaultLabel
		}
		id, plaintext, err := store.issue(label, workspace)
		if err != nil {
			pushError(w, http.StatusInternalServerError, "issuing device token failed")
			return
		}
		pushJSON(w, http.StatusOK, redeemResp{Token: plaintext, TokenID: id})
	}
}
