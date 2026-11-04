import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { t } from '../lib/i18n';
import { InfoIcon } from './icons';
import styles from './InfoTip.module.css';

export interface InfoTipProps {
  text: string;
  /** Accessible name of the trigger; defaults to "Mais informações". */
  label?: string;
}

const HIDE_DELAY_MS = 150;

/**
 * Tooltip that satisfies WCAG 2.2 1.4.13: opens on hover, focus and tap, can be dismissed with
 * Esc without moving focus, stays open while the pointer is over it, and never hides the trigger.
 */
export function InfoTip({ text, label }: InfoTipProps) {
  const id = useId();
  const wrapRef = useRef<HTMLSpanElement>(null);
  const timer = useRef<number | null>(null);
  // Touch taps also emit emulated mouseenter/click, so the pointer type decides the behaviour:
  // mouse hovers open, keyboard focus opens, touch taps toggle.
  const lastPointer = useRef('mouse');
  const [open, setOpen] = useState(false);
  const [align, setAlign] = useState<'start' | 'end'>('start');

  const cancelHide = useCallback(() => {
    if (timer.current !== null) {
      window.clearTimeout(timer.current);
      timer.current = null;
    }
  }, []);

  const show = useCallback(() => {
    cancelHide();
    const rect = wrapRef.current?.getBoundingClientRect();
    if (rect) setAlign(rect.left + rect.width / 2 > window.innerWidth / 2 ? 'end' : 'start');
    setOpen(true);
  }, [cancelHide]);

  const hideSoon = useCallback(() => {
    cancelHide();
    timer.current = window.setTimeout(() => {
      timer.current = null;
      setOpen(false);
    }, HIDE_DELAY_MS);
  }, [cancelHide]);

  useEffect(() => cancelHide, [cancelHide]);

  useEffect(() => {
    if (!open) return undefined;
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        setOpen(false);
      }
    };
    const onPointerDown = (event: PointerEvent): void => {
      if (!wrapRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('keydown', onKey);
    document.addEventListener('pointerdown', onPointerDown);
    return () => {
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('pointerdown', onPointerDown);
    };
  }, [open]);

  return (
    <span
      className={styles.wrap}
      ref={wrapRef}
      onPointerDown={(event) => {
        lastPointer.current = event.pointerType;
      }}
      onMouseEnter={() => {
        if (lastPointer.current !== 'touch') show();
      }}
      onMouseLeave={() => {
        if (lastPointer.current !== 'touch') hideSoon();
      }}
    >
      <button
        type="button"
        className={styles.trigger}
        aria-label={label ?? t('tooltips.more_info')}
        aria-describedby={id}
        aria-expanded={open}
        onClick={() => {
          if (lastPointer.current === 'touch' && open) setOpen(false);
          else show();
        }}
        onFocus={show}
        onBlur={hideSoon}
      >
        <InfoIcon size={18} />
      </button>
      <span role="tooltip" id={id} className={styles.tip} data-align={align} hidden={!open}>
        {text}
      </span>
    </span>
  );
}
