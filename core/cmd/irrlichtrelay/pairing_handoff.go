package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/skip2/go-qrcode"

	"irrlicht/core/pkg/onetimecode"
)

const missingPublicURLReason = "QR pairing is unavailable — start the relay with --public-url https://relay.example.com."

// missingPublicURLEnrollReason is missingPublicURLReason's enrollment
// counterpart (#1963): there is no QR anywhere on the enrollment path, so
// the one line explaining why no enrollment URL was built must name
// enrollment, not phone pairing, and say the code itself is not degraded —
// only the URL form is unavailable.
const missingPublicURLEnrollReason = "the enrollment URL is unavailable — start the relay with --public-url https://relay.example.com. The code itself still works when typed or pasted into the desktop app by hand."

// pairingHandoff is the relay-owned public origin for phone installation.
// The reason remains available when the origin is absent or invalid so both
// user interfaces can keep manual pairing and explain why no QR is present.
type pairingHandoff struct {
	publicURL         string
	unavailableReason string
}

func unavailablePairingHandoff(reason string) pairingHandoff {
	return pairingHandoff{unavailableReason: reason}
}

// resolvePairingHandoff accepts an HTTPS origin only. A path would make the
// fixed root API, manifest scope, and service-worker paths point at different
// applications, so it is rejected instead of silently trimmed.
func resolvePairingHandoff(raw string) pairingHandoff {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return unavailablePairingHandoff(missingPublicURLReason)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return invalidPairingHandoff()
	}
	if u.Scheme != "https" || u.Host == "" {
		return invalidPairingHandoff()
	}
	canonical := "https://" + u.Host
	normalized := "https" + raw[len(u.Scheme):]
	if normalized != canonical && normalized != canonical+"/" {
		return invalidPairingHandoff()
	}
	return pairingHandoff{publicURL: canonical}
}

func invalidPairingHandoff() pairingHandoff {
	return unavailablePairingHandoff("QR pairing is unavailable — --public-url must be one absolute HTTPS origin without a path, query, fragment, or credentials.")
}

func (h pairingHandoff) pairingURL(code string) string {
	if h.publicURL == "" || !onetimecode.IsPresentedCode(code) {
		return ""
	}
	return h.publicURL + "/pair/" + code + "/"
}

// enrollURL mirrors pairingURL for desktop enrollment (#1963): same origin,
// same guard (empty when --public-url is absent or code is not the exact
// presented XXXX-XXXX form Mint emits), but no trailing slash — the issue's
// own example is ".../enroll/K7QM-3PXA" — and no QR/handoff page: a desktop
// pastes this URL, it does not scan it.
func (h pairingHandoff) enrollURL(code string) string {
	if h.publicURL == "" || !onetimecode.IsPresentedCode(code) {
		return ""
	}
	return h.publicURL + "/enroll/" + code
}

// enrollUnavailableReason maps handoff's pairing-flavored unavailableReason
// to enrollment's own wording for the common case a default `serve`/
// `enroll new` hits — --public-url missing entirely (resolvePairingHandoff
// returns missingPublicURLReason for that, and only that, case; verified by
// reading resolvePairingHandoff above). The rarer case — a --public-url
// that was given but fails the format checks — is left as invalidPairingHandoff
// wrote it: "--public-url must be one absolute HTTPS origin..." already
// names the concrete fix independent of which feature is asking, so it is
// not misleading the way the QR-specific missing-URL text is.
func enrollUnavailableReason(h pairingHandoff) string {
	if h.unavailableReason == missingPublicURLReason {
		return missingPublicURLEnrollReason
	}
	return h.unavailableReason
}

// pairingQRDataURL returns a self-contained PNG. The authenticated mint
// response can therefore give the same image bytes to the web and macOS
// surfaces without publishing a second endpoint for the one-time code.
func pairingQRDataURL(pairingURL string) (string, error) {
	png, err := qrcode.Encode(pairingURL, qrcode.Medium, 256)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

var pairingPageTemplate = template.Must(template.New("pairing-handoff").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="theme-color" content="#080c16">
  <title>Pair Irrlicht Elfdans</title>
  <link rel="manifest" href="manifest.webmanifest">
  <link rel="icon" href="/elfdans-icon.svg">
  <link rel="stylesheet" href="/irrlicht.css">
</head>
<body class="pairing-handoff-page">
  <main class="pairing-handoff-card">
    <img src="/elfdans-icon.svg" alt="" width="72" height="72">
    <h1>Pair Irrlicht Elfdans</h1>
    <p>Add this page to your Home Screen. Then open Elfdans and press <strong>Pair this phone</strong>.</p>
    <p class="pairing-handoff-code">{{.Code}}</p>
    <p>This code expires after 10 minutes. If it expires, create a new QR code on the Mac.</p>
  </main>
  <script type="module" src="/pair-handoff.js"></script>
</body>
</html>`))

func registerPairingHandoffRoutes(mux *http.ServeMux, handoff pairingHandoff) {
	mux.HandleFunc("GET /pair/{code}/{$}", func(w http.ResponseWriter, r *http.Request) {
		code := r.PathValue("code")
		if handoff.pairingURL(code) == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pairingPageTemplate.Execute(w, struct{ Code string }{Code: code})
	})
	mux.HandleFunc("GET /pair/{code}/manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		code := r.PathValue("code")
		if handoff.pairingURL(code) == "" {
			http.NotFound(w, r)
			return
		}
		manifest := struct {
			Name            string `json:"name"`
			ShortName       string `json:"short_name"`
			ID              string `json:"id"`
			StartURL        string `json:"start_url"`
			Scope           string `json:"scope"`
			Display         string `json:"display"`
			ThemeColor      string `json:"theme_color"`
			BackgroundColor string `json:"background_color"`
			Icons           []any  `json:"icons"`
		}{
			Name: "Irrlicht Elfdans", ShortName: "Elfdans", ID: "/",
			StartURL: "/pair/" + code + "/", Scope: "/", Display: "standalone",
			ThemeColor: "#080c16", BackgroundColor: "#080c16",
			Icons: []any{map[string]string{"src": "/elfdans-icon.svg", "sizes": "any", "type": "image/svg+xml"}},
		}
		w.Header().Set("Content-Type", "application/manifest+json")
		_ = json.NewEncoder(w).Encode(manifest)
	})
}

// registerEnrollHandoffRoute wires the desktop enrollment URL form (#1963):
// unlike /pair/{code}/, this is not a PWA install page — no manifest, no
// icon, no script — just enough body that a person who follows the link by
// hand (instead of pasting it into the desktop app, which never issues a
// GET here) can read the code and what to do with it. Registered
// unconditionally, like registerPairingHandoffRoutes: it touches no store,
// so it carries no auth gate of its own — the --auth off 403 lives on
// registerEnrollRoutes' API routes instead, where the enrollment store is
// actually read.
func registerEnrollHandoffRoute(mux *http.ServeMux, handoff pairingHandoff) {
	mux.HandleFunc("GET /enroll/{code}", func(w http.ResponseWriter, r *http.Request) {
		code := r.PathValue("code")
		// enrollURL guards with onetimecode.IsPresentedCode before this
		// handler ever writes code into the response body below — garbage
		// path input 404s here, before any reflection.
		if handoff.enrollURL(code) == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Irrlicht enrollment code: %s\n\nPaste this URL into the desktop app's enrollment field to finish joining this relay. The code expires 10 minutes after it was minted and can be used once.\n", code)
	})
}
