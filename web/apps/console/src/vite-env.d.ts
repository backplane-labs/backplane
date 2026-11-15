/// <reference types="vite/client" />

// Makes this an ambient module file so the react augmentation below merges instead of shadowing.
export {};

// CSS custom properties the console sets through inline styles; declaring them keeps
// the style objects typable without casts.
declare module 'react' {
  interface CSSProperties {
    '--navigation-width'?: string;
  }
}
