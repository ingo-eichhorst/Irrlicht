// The scanned page becomes the installed app's code-specific start URL. Only
// the installed app moves to the root dashboard; Safari stays on the page that
// offers Add to Home Screen.
export function pairingCodeFromPath(pathname) {
  const match = String(pathname || '').match(/^\/pair\/([ABCDEFGHJKMNPQRSTVWXYZ23456789]{4}-[ABCDEFGHJKMNPQRSTVWXYZ23456789]{4})\/$/);
  return match ? match[1] : '';
}

export function isInstalledApp(media = window.matchMedia, standalone = navigator.standalone) {
  return standalone === true || media('(display-mode: standalone)').matches;
}

export function continueInstalledPairing(locationObject = window.location) {
  const code = pairingCodeFromPath(locationObject.pathname);
  if (!code || !isInstalledApp()) return false;
  locationObject.replace('/?pair=' + encodeURIComponent(code));
  return true;
}

if (typeof window !== 'undefined') continueInstalledPairing();
