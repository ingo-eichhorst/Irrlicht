package main

import (
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/skip2/go-qrcode"

	"irrlicht/core/cmd/irrlichtrelay/push"
)

const missingPublicURLReason = "QR pairing is unavailable — start the relay with --public-url https://relay.example.com."

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
	if h.publicURL == "" || !push.IsPresentedCode(code) {
		return ""
	}
	return h.publicURL + "/pair/" + code + "/"
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
