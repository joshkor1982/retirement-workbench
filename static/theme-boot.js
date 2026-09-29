/* Pre-paint theme: runs in <head> so the page never flashes the wrong theme.
   One storage key, "rwTheme", in {light, system, dark}. data-theme on <html>
   is always the RESOLVED value (light or dark). ?theme=light|dark|system in
   the URL overrides for this load only, for screenshots. */
(function () {
  var MODES = { light: 1, system: 1, dark: 1 };
  function resolve(mode) {
    if (mode === 'light' || mode === 'dark') return mode;
    try {
      return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
    } catch (e) { return 'dark'; }
  }
  var mode = null;
  try {
    var q = new URLSearchParams(location.search).get('theme');
    if (q && MODES[q]) mode = q;
  } catch (e) {}
  if (!mode) {
    try {
      mode = localStorage.getItem('rwTheme');
      if (!MODES[mode]) {
        mode = 'system';
        localStorage.setItem('rwTheme', mode);
      }
    } catch (e) { mode = 'system'; }
  }
  document.documentElement.setAttribute('data-theme', resolve(mode));
})();
