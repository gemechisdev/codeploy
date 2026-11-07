/**
 * English UI strings. Keys are the stable contract consumed by `t()` / `useT()`;
 * only the values are copy. A future locale just supplies another `Dictionary`.
 * No runtime loading, no external URLs, no dependencies.
 */
export const en = {
  'app.title': 'codeplo',

  // Navigation (static NAV entries in features/shell/nav.ts)
  'nav.group.server': 'Server',
  'nav.dashboard': 'Dashboard',
  'nav.projects': 'Projects',
  'nav.security': 'Security',
  'nav.terminal': 'Terminal',
  'nav.proxy': 'Proxy & Domains',
  'nav.monitoring': 'Monitoring',
  'nav.templates': 'Templates',
  'nav.extensions': 'Extensions',
  'nav.previews': 'Previews',
  'nav.notifications': 'Notifications',
  'nav.backups': 'Backups',
  'nav.settings': 'Settings',

  // Shell / sidebar / header
  'shell.mainNav': 'Main',
  'shell.navigation': 'Navigation',
  'shell.navigationDescription': 'Primary navigation menu',
  'shell.openNavigation': 'Open navigation',
  'shell.closeNavigation': 'Close navigation',
  'shell.skipToContent': 'Skip to content',
  'shell.breadcrumb': 'Breadcrumb',
  'shell.controlPlane': 'control plane',
  'shell.securityScore': 'Security score',
  'shell.bootstrap': 'Bootstrap session (SSH tunnel). Configure HTTPS in Settings to serve the panel on a domain.',
  'header.projects': 'Projects',
  'header.dashboard': 'Dashboard',
  'header.elevated': 'Elevated',
  'header.elevatedTitle': 'Elevated by a recent 2FA check',
  'header.search': 'Search',
  'header.openPalette': 'Open command palette (Ctrl+K)',
  'header.themeToLight': 'Switch to light theme',
  'header.themeToDark': 'Switch to dark theme',
  'header.userMenu': 'User menu',
  'header.signOut': 'Sign out',

  // Command palette
  'palette.title': 'Command palette',
  'palette.description': 'Jump to a page or resource',
  'palette.searchLabel': 'Search pages and resources',
  'palette.placeholder': 'Search pages and resources…',
  'palette.results': 'Results',
  'palette.noMatches': 'No matches',
  'palette.page': 'Page',

  // Query boundaries
  'state.nothingHere': 'Nothing here yet',

  // Shared dialogs / toasts
  'dialog.close': 'Close dialog',
  'toast.notifications': 'Notifications',
  'toast.dismiss': 'Dismiss notification',
  'confirm.cancel': 'Cancel',
  'confirm.confirm': 'Confirm',
} as const;

export type TranslationKey = keyof typeof en;
/** Every locale must supply the complete, stable key set. */
export type Dictionary = Readonly<Record<TranslationKey, string>>;
