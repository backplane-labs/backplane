/** Backplane mark: three service layers joined by one bus. */
export function BrandMark({ size = 22 }: { size?: number }) {
  return <svg className="console-brand-mark" width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <rect x="1.5" y="1.5" width="21" height="21" rx="5" fill="currentColor" fillOpacity=".12" stroke="currentColor" strokeOpacity=".45" />
    <path d="M7 7.5h10M7 12h10M7 16.5h10" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
    <path d="M12 6v12" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeOpacity=".55" />
    <circle cx="12" cy="7.5" r="1.6" fill="currentColor" /><circle cx="12" cy="12" r="1.6" fill="currentColor" /><circle cx="12" cy="16.5" r="1.6" fill="currentColor" />
  </svg>;
}
