export const serviceTabs = ['overview', 'configuration', 'telemetry', 'errors', 'audit'] as const;
export type ServiceTab = (typeof serviceTabs)[number];
const tabs = new Set<string>(serviceTabs);
export const isServiceTab = (value: string): value is ServiceTab => tabs.has(value);
