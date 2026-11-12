// The only host APIs the core may use besides its ports: timers and UTF-8
// encoding, present in browsers, workers, React Native (Hermes) and Node.
// tsconfig.core.json checks the core against these alone, with no DOM.
declare function setTimeout(handler: () => void, ms?: number): number
declare function clearTimeout(id: number | undefined): void
declare function setInterval(handler: () => void, ms?: number): number
declare function clearInterval(id: number | undefined): void
declare class TextEncoder {
  encode(input?: string): Uint8Array
}
