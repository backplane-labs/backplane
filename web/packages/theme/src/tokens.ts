// Palette adapted from HyperDX; see ../NOTICE.md for source and license.
export const palette = {
  green: ['#eafff6', '#cdfee7', '#a0fad5', '#63f2bf', '#25e2a5', '#00c28a', '#00a475', '#008362', '#00674e', '#005542'],
  gray: ['#fafafa', '#e6e6ee', '#d7d8db', '#aeaeb7', '#a1a1aa', '#868691', '#7e7e8b', '#6c6c79', '#5f5f6e', '#515264'],
  dark: ['#c1c2c5', '#a6a7ab', '#909296', '#5c5f66', '#373a40', '#2c2e33', '#25262b', '#1a1b1e', '#141517', '#101113'],
} as const;

export type ThemeMode = 'dark' | 'light';
export const typography = {
  fontFamily: '"IBM Plex Sans", sans-serif',
  fontFamilyMonospace: '"IBM Plex Mono", monospace',
  fontSize: 14,
  htmlFontSize: 16,
} as const;

// Surfaces, from back to front: background (page) < chrome (header, sidebar)
// < card (panels) < raised (hover, selected rows, table headers).
export const tokens = {
  dark: {
    background: palette.dark[9], foreground: '#d5d6d9',
    chrome: palette.dark[8], card: palette.dark[7], 'card-foreground': '#d5d6d9', raised: palette.dark[6],
    popover: palette.dark[7], 'popover-foreground': '#d5d6d9',
    primary: palette.green[4], 'primary-foreground': palette.dark[9],
    secondary: palette.dark[6], 'secondary-foreground': '#d5d6d9',
    muted: palette.dark[8], 'muted-foreground': palette.dark[2],
    subtle: palette.dark[3],
    accent: palette.dark[6], 'accent-foreground': '#e9eaec',
    destructive: '#ff6b6b', 'destructive-foreground': '#2b0a0a',
    success: palette.green[4], 'success-foreground': '#0d2913',
    warning: '#fcc419', 'warning-foreground': '#3c3310',
    info: '#4dabf7', 'info-foreground': '#071e34',
    border: palette.dark[5], 'border-strong': palette.dark[4], input: palette.dark[4], ring: palette.green[5],
    link: palette.green[3], radius: '4px',
    'chart-1': '#25e2a5', 'chart-2': '#4dabf7', 'chart-3': '#fcc419', 'chart-4': '#ff8787', 'chart-5': '#b197fc',
    // Code syntax is text: at least 4.5:1 on the editor's surfaces.
    'syntax-key': '#4dabf7', 'syntax-string': '#25e2a5', 'syntax-number': '#fcc419', 'syntax-tag': '#ff8787', 'syntax-literal': '#b197fc',
  },
  light: {
    background: '#f7f7f9', foreground: '#1a1b1e',
    chrome: '#ffffff', card: '#ffffff', 'card-foreground': '#1a1b1e', raised: '#f1f1f4',
    popover: '#ffffff', 'popover-foreground': '#1a1b1e',
    primary: palette.green[7], 'primary-foreground': '#ffffff',
    secondary: '#f1f1f4', 'secondary-foreground': '#1a1b1e',
    muted: '#f7f7f9', 'muted-foreground': palette.gray[7],
    subtle: palette.gray[4],
    accent: '#ebebef', 'accent-foreground': '#101113',
    destructive: '#b02525', 'destructive-foreground': '#ffffff',
    success: '#066b4c', 'success-foreground': '#eafff6',
    warning: '#9a4d00', 'warning-foreground': '#fff9db',
    info: '#1864ab', 'info-foreground': '#e7f5ff',
    border: '#e4e4ea', 'border-strong': palette.gray[2], input: palette.gray[2], ring: palette.green[6],
    link: palette.green[8], radius: '4px',
    'chart-1': '#0ca678', 'chart-2': '#1c7ed6', 'chart-3': '#f08c00', 'chart-4': '#e03131', 'chart-5': '#7048e8',
    // Code syntax is text: at least 4.5:1 on the editor's surfaces (the
    // chart hues are for graphics, 3:1).
    'syntax-key': '#1864ab', 'syntax-string': '#066b4c', 'syntax-number': '#9a4d00', 'syntax-tag': '#b02525', 'syntax-literal': '#5f3dc4',
  },
} as const;

export function cssVariables(mode: ThemeMode): Record<string, string> {
  return {
    ...Object.fromEntries(Object.entries(tokens[mode]).map(([key, value]) => [`--${key}`, value])),
    '--font-sans': typography.fontFamily,
    '--font-mono': typography.fontFamilyMonospace,
  };
}
