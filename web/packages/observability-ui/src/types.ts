export type Attributes = Readonly<Record<string, unknown>>;
export interface LogRecord {
  id: string;
  timestamp: string;
  severity?: string;
  body: unknown;
  attributes?: Attributes;
  resource?: Attributes;
  traceId?: string;
  spanId?: string;
}
export interface SpanEvent {
  name: string;
  timestamp: string;
  attributes?: Attributes;
}
export interface SpanLink {
  traceId: string;
  spanId: string;
  attributes?: Attributes;
}
export interface SpanRecord {
  id: string;
  traceId: string;
  parentId?: string;
  name: string;
  service?: string;
  startUnixNano: string;
  endUnixNano: string;
  status?: string;
  kind?: string;
  attributes?: Attributes;
  resource?: Attributes;
  events?: readonly SpanEvent[];
  links?: readonly SpanLink[];
}
export interface TraceSummary {
  id: string;
  name: string;
  service?: string;
  startUnixNano: string;
  durationNanos: string;
  error?: boolean;
}
export interface SourceLine {
  number: number;
  text: string;
}
export interface StackFrame {
  id: string;
  function?: string;
  file?: string;
  line?: number;
  column?: number;
  inApp?: boolean;
  source?: readonly SourceLine[];
}
export interface ErrorCause {
  id: string;
  type?: string;
  message: string;
  frames?: readonly StackFrame[];
  cause?: ErrorCause;
  payload?: unknown;
}
export interface CorrelationTarget {
  signal: 'logs' | 'metrics' | 'traces';
  traceId?: string;
  spanId?: string;
  resource?: Attributes;
  timestamp?: string;
}
