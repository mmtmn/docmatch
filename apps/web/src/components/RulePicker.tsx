import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';
import { useCharLimit } from '../hooks/useCharLimit';
import { t } from '../lib/i18n';
import { CharCounter } from './CharCounter';
import { CheckIcon, ChevronIcon, SearchIcon } from './icons';
import styles from './RulePicker.module.css';

export interface RuleOption {
  id: string;
  title: string;
  /** Rules the account used in a previous run; listed first and preselected. */
  usedBefore?: boolean;
}

export interface RulePickerProps {
  options: readonly RuleOption[];
  selected: readonly string[];
  onChange: (ids: string[]) => void;
  /** limits.text.rule_picker_search */
  searchMax: number;
  disabled?: boolean;
}

function normalize(value: string): string {
  return value.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
}

/**
 * Multi-select popover with a search field. Keyboard: Enter/Space on the trigger opens and focuses
 * the search; ArrowDown moves to the list; arrows move the active option; Space/Enter toggles;
 * Esc closes and returns focus. The parent only renders it when the account has saved rules.
 */
export function RulePicker({
  options,
  selected,
  onChange,
  searchMax,
  disabled = false,
}: RulePickerProps) {
  const id = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const limit = useCharLimit(query, searchMax);

  const needle = normalize(query.trim());
  const visible = needle ? options.filter((o) => normalize(o.title).includes(needle)) : options;
  const groups = [
    { key: 'recent', label: t('picker.group_recent'), items: visible.filter((o) => o.usedBefore) },
    { key: 'all', label: t('picker.group_all'), items: visible.filter((o) => !o.usedBefore) },
  ].filter((group) => group.items.length > 0);
  const flat = groups.flatMap((group) => group.items);
  const activeOption = flat[Math.min(active, Math.max(0, flat.length - 1))];
  const optionId = (ruleId: string): string => `${id}-opt-${ruleId}`;

  const close = (refocus: boolean): void => {
    setOpen(false);
    setQuery('');
    setActive(0);
    if (refocus) triggerRef.current?.focus();
  };

  const toggle = (ruleId: string): void => {
    onChange(
      selected.includes(ruleId)
        ? selected.filter((item) => item !== ruleId)
        : [...selected, ruleId],
    );
  };

  useEffect(() => {
    if (open) searchRef.current?.focus();
  }, [open]);

  useEffect(() => {
    if (!open) return undefined;
    const onKey = (event: globalThis.KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        close(true);
      }
    };
    const onPointerDown = (event: PointerEvent): void => {
      if (!rootRef.current?.contains(event.target as Node)) close(false);
    };
    document.addEventListener('keydown', onKey);
    document.addEventListener('pointerdown', onPointerDown);
    return () => {
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('pointerdown', onPointerDown);
    };
  }, [open]);

  useEffect(() => {
    if (!open || !activeOption) return;
    document.getElementById(optionId(activeOption.id))?.scrollIntoView({ block: 'nearest' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, activeOption?.id]);

  const onListKey = (event: KeyboardEvent<HTMLElement>): void => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        setActive((index) => Math.min(index + 1, Math.max(0, flat.length - 1)));
        if (event.currentTarget !== listRef.current) listRef.current?.focus();
        break;
      case 'ArrowUp':
        event.preventDefault();
        setActive((index) => Math.max(index - 1, 0));
        break;
      case 'Home':
        event.preventDefault();
        setActive(0);
        break;
      case 'End':
        event.preventDefault();
        setActive(Math.max(0, flat.length - 1));
        break;
      case ' ':
      case 'Enter':
        if (event.currentTarget === listRef.current && activeOption) {
          event.preventDefault();
          toggle(activeOption.id);
        }
        break;
      default:
        break;
    }
  };

  const selectedCount = selected.length;

  return (
    <div className={styles.picker} ref={rootRef}>
      <button
        ref={triggerRef}
        type="button"
        className={styles.trigger}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? `${id}-dialog` : undefined}
        disabled={disabled}
        onClick={() => (open ? close(false) : setOpen(true))}
      >
        <span>
          {selectedCount > 0
            ? t('picker.trigger_with_count', { count: selectedCount })
            : t('picker.trigger')}
        </span>
        <ChevronIcon size={16} />
      </button>

      {open && (
        <div
          role="dialog"
          id={`${id}-dialog`}
          aria-label={t('picker.label')}
          className={styles.dialog}
        >
          <div className={styles.search}>
            <SearchIcon size={16} className={styles.searchIcon} />
            <input
              ref={searchRef}
              type="search"
              className={styles.searchInput}
              value={query}
              maxLength={searchMax}
              placeholder={t('picker.search_placeholder')}
              aria-label={t('picker.search_label')}
              aria-describedby={`${id}-counter`}
              aria-controls={`${id}-list`}
              onChange={(event) => {
                setQuery(event.target.value);
                setActive(0);
              }}
              onKeyDown={onListKey}
            />
          </div>
          <CharCounter id={`${id}-counter`} state={limit} />

          <div
            ref={listRef}
            id={`${id}-list`}
            role="listbox"
            aria-multiselectable="true"
            aria-label={t('picker.listbox_label')}
            aria-activedescendant={activeOption ? optionId(activeOption.id) : undefined}
            tabIndex={0}
            className={styles.list}
            onKeyDown={onListKey}
          >
            {groups.map((group) => (
              <div role="group" key={group.key} aria-labelledby={`${id}-group-${group.key}`}>
                <div id={`${id}-group-${group.key}`} className={styles.groupLabel}>
                  {group.label}
                </div>
                {group.items.map((option) => {
                  const index = flat.indexOf(option);
                  const isSelected = selected.includes(option.id);
                  return (
                    // The listbox handles keyboard interaction via aria-activedescendant.
                    // eslint-disable-next-line jsx-a11y-x/click-events-have-key-events
                    <div
                      role="option"
                      id={optionId(option.id)}
                      key={option.id}
                      aria-selected={isSelected}
                      tabIndex={-1}
                      data-active={index === active ? 'true' : undefined}
                      className={styles.option}
                      onMouseEnter={() => setActive(index)}
                      onClick={() => {
                        setActive(index);
                        toggle(option.id);
                      }}
                    >
                      <span className={styles.check} aria-hidden="true">
                        {isSelected && <CheckIcon size={16} />}
                      </span>
                      <span className={styles.optionTitle}>{option.title}</span>
                    </div>
                  );
                })}
              </div>
            ))}
            {flat.length === 0 && <p className={styles.empty}>{t('picker.no_results')}</p>}
          </div>

          <div className={styles.footer}>
            <span className={styles.count} aria-live="polite">
              {t('picker.selected_count', { count: selectedCount })}
            </span>
            <button type="button" className={styles.footerButton} onClick={() => onChange([])}>
              {t('picker.clear')}
            </button>
            <button type="button" className={styles.footerButton} onClick={() => close(true)}>
              {t('picker.close')}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
