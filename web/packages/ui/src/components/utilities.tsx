import type { LucideIcon } from 'lucide-react';
import {
  Component,
  type ComponentProps,
  type ErrorInfo,
  type ReactNode,
  type RefObject,
  useEffect,
  useRef,
  useState,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Avatar, AvatarFallback, AvatarImage } from './avatar.js';
import { Badge } from './badge.js';
import { Button } from './button.js';
import { Combobox, type ComboboxProps } from './combobox.js';
import { Tooltip, TooltipContent, TooltipTrigger } from './tooltip.js';

export function Icon({
  icon: Glyph,
  label,
  size = 16,
  ...props
}: { icon: LucideIcon; label?: string; size?: number } & Omit<ComponentProps<LucideIcon>, 'size' | 'aria-label'>) {
  return (
    <Glyph
      {...props}
      size={size}
      aria-label={label}
      role={label ? 'img' : undefined}
      aria-hidden={label ? undefined : true}
    />
  );
}
export function UserAvatar({ name, src }: { name: string; src?: string }) {
  const initials = name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((word) => [...word][0] ?? '')
    .join('')
    .toUpperCase();
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} role="img" aria-label={name}>
          <Avatar>
            <AvatarImage src={src} alt={name} />
            <AvatarFallback aria-hidden="true">{initials}</AvatarFallback>
          </Avatar>
        </span>
      </TooltipTrigger>
      <TooltipContent>{name}</TooltipContent>
    </Tooltip>
  );
}
export function UsersIndicator({
  users,
  max = 4,
  label,
}: {
  users: readonly { id: string; name: string; src?: string }[];
  max?: number;
  label: string;
}) {
  const bound = Math.max(1, Math.floor(max));
  return (
    <div role="group" aria-label={label} style={{ display: 'flex', gap: 4 }}>
      {users.slice(0, bound).map((user) => (
        <UserAvatar key={user.id} name={user.name} src={user.src} />
      ))}
      {users.length > bound && (
        <span
          title={users
            .slice(bound)
            .map((user) => user.name)
            .join(', ')}
        >
          +{users.length - bound}
        </span>
      )}
    </div>
  );
}
export function FeatureBadge({ children }: { children: ReactNode }) {
  return <Badge variant="secondary">{children}</Badge>;
}
export function FilterPill({
  label,
  value,
  onRemove,
  disabled,
}: {
  label: string;
  value: string;
  onRemove?: () => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation('backplane.ui');
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 4,
        border: '1px solid var(--border)',
        borderRadius: 4,
        paddingLeft: 8,
      }}
    >
      {label}: {value}
      {onRemove && (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          disabled={disabled}
          aria-label={t('removeValue', { name: label })}
          onClick={onRemove}
        >
          ×
        </Button>
      )}
    </span>
  );
}
/** A compact static or async segment uses the same accessible selection control. */
export function Segment(props: ComboboxProps) {
  return <Combobox {...props} />;
}

export function useClickOutside(
  refs: readonly RefObject<HTMLElement | null>[],
  onOutside: (event: PointerEvent) => void,
  enabled = true,
) {
  const current = useRef({ refs, onOutside });
  current.current = { refs, onOutside };
  useEffect(() => {
    if (!enabled) return;
    const listener = (event: PointerEvent) => {
      const path = event.composedPath();
      if (current.current.refs.every((ref) => !ref.current || !path.includes(ref.current)))
        current.current.onOutside(event);
    };
    document.addEventListener('pointerdown', listener);
    return () => document.removeEventListener('pointerdown', listener);
  }, [enabled]);
}
export function useDelayedSwitch(value: boolean, showDelay = 150, hideDelay = 0) {
  const [visible, setVisible] = useState(value && showDelay === 0);
  useEffect(() => {
    const timer = setTimeout(() => setVisible(value), Math.max(0, value ? showDelay : hideDelay));
    return () => clearTimeout(timer);
  }, [value, showDelay, hideDelay]);
  return visible;
}
interface BoundaryProps {
  children: ReactNode;
  resetKey?: unknown;
  fallback: (error: Error, reset: () => void) => ReactNode;
  onError?: (error: Error, info: ErrorInfo) => void;
}
export class ErrorBoundary extends Component<BoundaryProps, { error?: Error }> {
  state: { error?: Error } = {};
  static getDerivedStateFromError(error: unknown) {
    return { error: error instanceof Error ? error : new Error(String(error)) };
  }
  componentDidCatch(error: Error, info: ErrorInfo) {
    this.props.onError?.(error, info);
  }
  componentDidUpdate(previous: BoundaryProps) {
    if (!Object.is(previous.resetKey, this.props.resetKey) && this.state.error) this.setState({ error: undefined });
  }
  render() {
    return this.state.error
      ? this.props.fallback(this.state.error, () => this.setState({ error: undefined }))
      : this.props.children;
  }
}
