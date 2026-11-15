/** Decimal arithmetic for controls. Values never pass through IEEE-754. */
export function decimal(value: string): { coefficient: bigint; scale: number } | undefined {
  const match = /^([+-]?)(?:(\d+)(?:\.(\d*))?|\.(\d+))$/.exec(value);
  if (!match) return undefined;
  const fraction = match[3] ?? match[4] ?? '';
  return { coefficient: BigInt(`${match[1] === '-' ? '-' : ''}${match[2] ?? '0'}${fraction}`), scale: fraction.length };
}

export function stepDecimal(value: string, step: string, direction: 1 | -1, min?: string, max?: string): string {
  const base = decimal(value || '0'),
    delta = decimal(step);
  const lower = min === undefined ? undefined : decimal(min);
  const upper = max === undefined ? undefined : decimal(max);
  if (!base || !delta || (min !== undefined && !lower) || (max !== undefined && !upper)) return value;
  const scale = Math.max(base.scale, delta.scale, lower?.scale ?? 0, upper?.scale ?? 0);
  const expand = (number: { coefficient: bigint; scale: number }) =>
    number.coefficient * 10n ** BigInt(scale - number.scale);
  let result = expand(base) + expand(delta) * BigInt(direction);
  if (lower && result < expand(lower)) result = expand(lower);
  if (upper && result > expand(upper)) result = expand(upper);
  const sign = result < 0n ? '-' : '';
  const digits = (result < 0n ? -result : result).toString().padStart(scale + 1, '0');
  return sign + (scale ? `${digits.slice(0, -scale)}.${digits.slice(-scale)}`.replace(/\.?0+$/, '') : digits);
}
