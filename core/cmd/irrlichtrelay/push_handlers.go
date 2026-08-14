package main

// The push HTTP surface: pairing, subscription registration, and feature
// detection (docs/mobile-notifications-arc42.md §6.1, §8.1). Everything
// stateful lives in the push package; this file decides status codes and
// wires the bearer-token gate.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"irrlicht/core/cmd/irrlichtrelay/push"
	"irrlicht/core/pkg/webpush"
)

// pushAuthRequired names the fix wherever push is refused on an anonymous
// relay: a push-capable relay is by definition reachable, so anonymous mode
// and push are mutually exclusive (docs/mobile-notifications-arc42.md §8.1)
// — one security model, not two, even on a tailnet.
const pushAuthRequired = "push requires --auth tokens-file (anonymous mode and push are mutually exclusive)"

// pushBodyLimit bounds a push request body. The pair endpoint is
// pre-authentication (the code is the credential) and a PushSubscription is
// under a kilobyte, so anything near the limit is garbage, not a client.
const pushBodyLimit = 64 << 10

// pairDefaultLabel names a device token whose pairing request carried no
// label, so `token list` still reads sensibly.
const pairDefaultLabel = "beacon"

// pairCodeInvalidMsg is the single uniform pairing failure the wire ever
// sees: unknown, expired and already-used codes answer identically, so the
// endpoint is no oracle for which codes exist.
const pairCodeInvalidMsg = "invalid or expired pairing code"

// maxPairLabelRunes caps a device label: it lands verbatim in tokens.json
// and `token list` prints it into the operator's terminal, so it gets a
// length bound and no control characters (ANSI escapes included) — a paired
// phone holds a valid code, not a license to redecorate a terminal.
const maxPairLabelRunes = 64

// buildPushService constructs the relay's push service for an auth-enabled
// relay, or nil with --auth off — the construction half of the
// push-requires-auth guard: no push state, in particular no VAPID key
// material, is created for a mode that cannot use it
// (docs/mobile-notifications-arc42.md §8.1). It fatals rather than
// regenerating over an unloadable file; each loader's error carries the
// remedy for ITS file — the two differ (a corrupt VAPID identity risks
// re-pairing every phone, a corrupt registry is safe to delete because
// phones re-subscribe), and one message covering both steered the operator
// toward the expensive fix.
func buildPushService(store *authStore, ddir string) *push.Service {
	if store == nil {
		return nil
	}
	svc, err := push.NewService(ddir, nil)
	if err != nil {
		log.Fatalf("push: %v", err)
	}
	return svc
}

// registerPushRoutes wires the push endpoints onto mux. svc is nil exactly
// when auth is off, and that case is the serving half of the
// push-requires-auth guard (docs/mobile-notifications-arc42.md §8.1): the
// info endpoint answers enabled:false with the reason, and every other push
// route is a 403 naming the fix.
func registerPushRoutes(mux *http.ServeMux, store *authStore, svc *push.Service) {
	if svc != nil && store == nil {
		// Held by construction in runServe (buildPushService returns nil
		// exactly when store is nil), but the invariant spans two functions
		// in two files: a second construction site wiring a service without
		// a store would serve every push mutation route unauthenticated,
		// all callers sharing the "" registry slot. Refuse loudly.
		panic("push routes require an auth store (docs/mobile-notifications-arc42.md §8.1)")
	}
	mux.HandleFunc("GET /api/v1/push/info", handlePushInfo(svc))
	if svc == nil {
		mux.HandleFunc("POST /api/v1/push/pairings", handlePushDisabled)
		mux.HandleFunc("POST /api/v1/push/pair", handlePushDisabled)
		mux.HandleFunc("POST /api/v1/push/subscriptions", handlePushDisabled)
		mux.HandleFunc("DELETE /api/v1/push/subscriptions", handlePushDisabled)
		mux.HandleFunc("GET /api/v1/push/subscriptions", handlePushDisabled)
		return
	}
	mux.HandleFunc("POST /api/v1/push/pairings", requireToken(store, handleMintPairing(svc)))
	mux.HandleFunc("POST /api/v1/push/pair", handlePair(svc, store))
	mux.HandleFunc("POST /api/v1/push/subscriptions", requireToken(store, handlePutSubscription(svc)))
	mux.HandleFunc("DELETE /api/v1/push/subscriptions", requireToken(store, handleDeleteSubscription(svc)))
	mux.HandleFunc("GET /api/v1/push/subscriptions", requireToken(store, handleSubscriptionStatus(svc)))
}

// pushJSON writes v as a JSON body with the given status.
func pushJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// pushError writes the {"error": …} shape every push failure uses.
func pushError(w http.ResponseWriter, status int, msg string) {
	type errorResp struct {
		Error string `json:"error"`
	}
	pushJSON(w, status, errorResp{Error: msg})
}

// handlePushDisabled refuses a push route on an anonymous relay, naming the
// fix (docs/mobile-notifications-arc42.md §8.1).
func handlePushDisabled(w http.ResponseWriter, _ *http.Request) {
	pushError(w, http.StatusForbidden, pushAuthRequired)
}

// handlePushInfo serves feature detection (docs/mobile-notifications-arc42.md
// §5.2): the pairing UI renders only where this answers enabled:true.
// Deliberately unauthenticated on both arms — with auth off it must be able
// to say WHY push is unavailable, and with auth on the VAPID public key is
// public by construction (it rides in every subscribe() call).
func handlePushInfo(svc *push.Service) http.HandlerFunc {
	type infoResp struct {
		Enabled        bool   `json:"enabled"`
		Reason         string `json:"reason,omitempty"`
		VAPIDPublicKey string `json:"vapid_public_key,omitempty"`
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		if svc == nil {
			pushJSON(w, http.StatusOK, infoResp{Enabled: false, Reason: pushAuthRequired})
			return
		}
		pushJSON(w, http.StatusOK, infoResp{Enabled: true, VAPIDPublicKey: svc.VAPIDPublicKey()})
	}
}

// handleMintPairing mints a pairing code in the caller's workspace — the
// bearer gate has already validated the client token, and the workspace
// rides from that token through the code into the device token issued at
// redeem, which is what lets the code "inherit a workspace"
// (docs/mobile-notifications-arc42.md §8.1).
func handleMintPairing(svc *push.Service) http.HandlerFunc {
	type mintResp struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		code, ttl, err := svc.MintCode(workspaceOf(r))
		if errors.Is(err, push.ErrTooManyCodes) {
			pushError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if err != nil {
			pushError(w, http.StatusInternalServerError, "minting pairing code failed")
			return
		}
		pushJSON(w, http.StatusCreated, mintResp{Code: code, ExpiresIn: int(ttl / time.Second)})
	}
}

// pairRequest is the body of POST /api/v1/push/pair. The route carries no
// bearer gate: the one-time code IS the credential
// (docs/mobile-notifications-arc42.md §6.1).
type pairRequest struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// handlePair redeems a pairing code for a device token — an ordinary
// TokenRecord in the code's workspace, so `token list` shows it and `token
// revoke` plus the prune is the full revocation story. The auth store is
// reloaded synchronously after issuing: the PWA subscribes with the new
// token immediately, and the 2 s watch tick would race that first request.
// Every redemption failure is one uniform 401 (no oracle); a saturated
// failure window is a 429.
func handlePair(svc *push.Service, store *authStore) http.HandlerFunc {
	type pairResp struct {
		Token          string `json:"token"`
		TokenID        string `json:"token_id"`
		VAPIDPublicKey string `json:"vapid_public_key"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req pairRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, pushBodyLimit)).Decode(&req); err != nil {
			pushError(w, http.StatusUnauthorized, pairCodeInvalidMsg)
			return
		}
		workspace, err := svc.Redeem(req.Code)
		if errors.Is(err, push.ErrRateLimited) {
			pushError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if err != nil {
			pushError(w, http.StatusUnauthorized, pairCodeInvalidMsg)
			return
		}
		label := sanitizeLabel(req.Label)
		if label == "" {
			label = pairDefaultLabel
		}
		id, plaintext, err := store.issue(label, workspace)
		if err != nil {
			pushError(w, http.StatusInternalServerError, "issuing device token failed")
			return
		}
		pushJSON(w, http.StatusOK, pairResp{Token: plaintext, TokenID: id, VAPIDPublicKey: svc.VAPIDPublicKey()})
	}
}

// sanitizeLabel drops control characters and caps the label at
// maxPairLabelRunes — see the constant for why the writer (a paired phone)
// is not trusted with the operator's terminal.
func sanitizeLabel(label string) string {
	kept := make([]rune, 0, maxPairLabelRunes)
	for _, r := range label {
		if unicode.IsControl(r) {
			continue
		}
		kept = append(kept, r)
		if len(kept) == maxPairLabelRunes {
			break
		}
	}
	return strings.TrimSpace(string(kept))
}

// handlePutSubscription stores the caller's PushSubscription.toJSON() under
// its authenticated token id, replacing any previous address (the §8.3
// self-heal). A malformed body is a 400 naming the offending field — refused
// at the door, never a stored dud that fails at dispatch time.
func handlePutSubscription(svc *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sub webpush.Subscription
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, pushBodyLimit)).Decode(&sub); err != nil {
			pushError(w, http.StatusBadRequest, "invalid subscription body: "+err.Error())
			return
		}
		if err := sub.Validate(); err != nil {
			pushError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := svc.SetSubscription(tokenIDOf(r), sub); err != nil {
			pushError(w, http.StatusInternalServerError, "storing subscription failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSubscriptionStatus serves the caller's own delivery health — the
// PWA health panel's data source (docs/mobile-notifications-arc42.md §8.3).
// endpoint_host is the endpoint's host and nothing more: the path is the
// subscription's capability secret (RFC 8030 §8.3) and must never leave the
// relay, not even to the phone that owns it. A null last_delivery means no
// send has been attempted since the relay started — health is RAM-only, and
// "unknown since restart" is the honest answer absence expresses (§8.6).
func handleSubscriptionStatus(svc *push.Service) http.HandlerFunc {
	type statusResp struct {
		Registered   bool                 `json:"registered"`
		Created      int64                `json:"created,omitempty"`
		EndpointHost string               `json:"endpoint_host,omitempty"`
		LastDelivery *push.DeliveryStatus `json:"last_delivery"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		resp := statusResp{}
		if st, ok := svc.DeliveryStatus(tokenIDOf(r)); ok {
			resp.LastDelivery = &st
		}
		if entry, ok := svc.SubscriptionOf(tokenIDOf(r)); ok {
			resp.Registered = true
			resp.Created = entry.Created
			if u, err := url.Parse(entry.Subscription.Endpoint); err == nil {
				resp.EndpointHost = u.Host
			}
		}
		pushJSON(w, http.StatusOK, resp)
	}
}

// handleDeleteSubscription removes the caller's own entry. Idempotent: a
// phone un-pairing twice gets a 204 both times.
func handleDeleteSubscription(svc *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := svc.DeleteSubscription(tokenIDOf(r)); err != nil {
			pushError(w, http.StatusInternalServerError, "removing subscription failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
