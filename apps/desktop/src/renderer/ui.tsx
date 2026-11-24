import { useId, useLayoutEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { AlertCircle, Check, Circle, Info, LoaderCircle, TriangleAlert, X } from 'lucide-react';
import type { Tone } from './core';

const toneIcon = { neutral: Circle, info: Info, good: Check, warn: TriangleAlert, error: AlertCircle };

export function Badge({ tone = 'neutral', children }: { tone?: Tone; children: ReactNode }) {
  const Icon = toneIcon[tone];
  return <span className={`badge tone-${tone}`}><Icon size={13} aria-hidden="true" />{children}</span>;
}

export function Notice({ tone = 'info', children }: { tone?: Tone; children: ReactNode }) {
  const Icon = toneIcon[tone];
  return <div className={`rd-notice tone-${tone}`} role={tone === 'error' ? 'alert' : undefined}>
    <Icon size={17} aria-hidden="true" /><div>{children}</div>
  </div>;
}

export function Spinner() {
  return <LoaderCircle className="rd-spinner" size={16} aria-hidden="true" />;
}

export function Modal({ title, description, children, onDismiss, busy, className = '' }: {
  title: string; description?: string; children: ReactNode; onDismiss: () => void; busy?: boolean; className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const descriptionId = useId();

  useLayoutEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (typeof dialog.showModal === 'function') dialog.showModal();
    else dialog.setAttribute('open', '');
    dialog.querySelector<HTMLElement>('[data-focus-initial]')?.focus();
    return () => {
      if (dialog.open && typeof dialog.close === 'function') dialog.close();
      if (trigger?.isConnected) trigger.focus({ preventScroll: true });
    };
  }, []);

  return <dialog ref={ref} className={`rd-dialog ${className}`} aria-modal="true" aria-labelledby={titleId} aria-describedby={description ? descriptionId : undefined}
    onCancel={(event) => { event.preventDefault(); if (!busy) onDismiss(); }}>
    <header className="rd-dialog-header">
      <div><h2 id={titleId}>{title}</h2>{description && <p id={descriptionId}>{description}</p>}</div>
      <button className="rd-icon" aria-label="关闭窗口" title="关闭窗口" disabled={busy} onClick={onDismiss}><X size={18} aria-hidden="true" /></button>
    </header>
    {children}
  </dialog>;
}

export function EmptyState({ icon, title, children, action }: { icon?: ReactNode; title: string; children: ReactNode; action?: ReactNode }) {
  return <div className="rd-empty">
    {icon && <span className="rd-empty-icon" aria-hidden="true">{icon}</span>}
    <div><strong>{title}</strong><p>{children}</p></div>
    {action}
  </div>;
}
