// External (not inline) so the strict CSP `script-src 'self'` holds. Applies the saved theme before first paint.
try {
  if (localStorage.getItem('codeplo_theme') === 'light') document.documentElement.classList.remove('dark');
} catch (e) { /* storage unavailable: keep the dark default */ }
