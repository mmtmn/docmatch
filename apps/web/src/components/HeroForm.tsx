import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type SubmitEvent,
} from 'react';
import { useVisualViewport } from '../hooks/useVisualViewport';
import { draftStore as defaultDraftStore, type DraftStore } from '../lib/draftStore';
import { t } from '../lib/i18n';
import type { FileCaps } from '../lib/limits';
import { precheckFiles, type AcceptedFile, type RejectedFile } from '../lib/precheck';
import { DropZone } from './DropZone';
import { CloseIcon } from './icons';
import type { RuleOption } from './RulePicker';
import { RulesBox } from './RulesBox';
import { RunButton, type Quote } from './RunButton';
import styles from './HeroForm.module.css';

export interface RunRequest {
  files: AcceptedFile[];
  rulesText: string;
  useRules: boolean;
  webSearch: boolean;
  selectedRules: string[];
}

export type QuoteProvider = (request: RunRequest, signal: AbortSignal) => Promise<Quote>;

export interface HeroFormProps {
  /** Saved rules of the account; the picker is hidden when there is no history. */
  ruleOptions?: readonly RuleOption[];
  /** Asks the API for an estimate whenever the request changes. */
  quoteProvider?: QuoteProvider;
  /** Starts the run. Rejections with a pt-BR message are shown to the user. */
  onSubmit?: (request: RunRequest) => Promise<void> | void;
  /** Plan caps, never looser than the shared hard caps. */
  caps?: Partial<FileCaps>;
  /** Keep an encrypted draft in IndexedDB (default true). */
  persistDraft?: boolean;
  /** Injectable store (tests). */
  store?: DraftStore;
}

type DraftStatus = 'loading' | 'ready' | 'restored' | 'unavailable';

const SAVE_DEBOUNCE_MS = 600;
const QUOTE_DEBOUNCE_MS = 400;

function dragHasFiles(event: DragEvent): boolean {
  return Array.from(event.dataTransfer?.types ?? []).includes('Files');
}

/**
 * The first screen: title, subtitle, drop zone, rules box, button, in a grid that always fits the
 * viewport (rows auto auto 1fr auto auto; the drop zone is the row that shrinks first).
 */
export function HeroForm({
  ruleOptions = [],
  quoteProvider,
  onSubmit,
  caps,
  persistDraft = true,
  store: injectedStore,
}: HeroFormProps) {
  const baseId = useId();
  const store = useMemo<DraftStore | null>(
    () => injectedStore ?? (persistDraft ? defaultDraftStore() : null),
    [injectedStore, persistDraft],
  );

  const [files, setFiles] = useState<AcceptedFile[]>([]);
  const [rejected, setRejected] = useState<RejectedFile[]>([]);
  const [rulesText, setRulesText] = useState('');
  const [useRules, setUseRules] = useState(true);
  const [webSearch, setWebSearch] = useState(false);
  const [selectedRules, setSelectedRules] = useState<string[]>(() =>
    ruleOptions.filter((option) => option.usedBefore).map((option) => option.id),
  );
  const [dragging, setDragging] = useState(false);
  const [draftStatus, setDraftStatus] = useState<DraftStatus>(store ? 'loading' : 'ready');
  const [notice, setNotice] = useState<string | null>(null);
  // The quote is keyed by a signature of the request, so a stale answer is never shown and no
  // state needs resetting inside the effect.
  const [quoteState, setQuoteState] = useState<{ key: string; quote: Quote | null }>({
    key: '',
    quote: null,
  });
  const [submitting, setSubmitting] = useState(false);
  const [online, setOnline] = useState(true);

  // Mirror of `files` for async code (precheck totals, paste); updated wherever files change.
  const filesRef = useRef<AcceptedFile[]>([]);
  const dragDepth = useRef(0);
  const dirty = useRef(false);
  const pasteCount = useRef(0);
  const queue = useRef<Promise<void>>(Promise.resolve());

  const viewport = useVisualViewport();
  const compact = viewport.keyboardOpen;

  const addFiles = useCallback(
    (incoming: File[]): Promise<void> => {
      if (incoming.length === 0) return Promise.resolve();
      // Serialised so two quick drops cannot both pass the per-run totals.
      queue.current = queue.current.then(async () => {
        const result = await precheckFiles(incoming, {
          existing: filesRef.current,
          ...(caps ? { caps } : {}),
        });
        if (result.accepted.length > 0) {
          filesRef.current = [...filesRef.current, ...result.accepted];
          setFiles(filesRef.current);
          dirty.current = true;
        }
        if (result.rejected.length > 0) setRejected((prev) => [...prev, ...result.rejected]);
      });
      return queue.current;
    },
    [caps],
  );

  const removeFile = useCallback((id: string): void => {
    dirty.current = true;
    filesRef.current = filesRef.current.filter((file) => file.id !== id);
    setFiles(filesRef.current);
  }, []);

  const dismissRejection = useCallback((id: string): void => {
    setRejected((prev) => prev.filter((item) => item.id !== id));
  }, []);

  // Restore the encrypted draft once.
  useEffect(() => {
    if (!store) return undefined;
    let cancelled = false;
    void (async () => {
      const available = await store.available();
      if (cancelled) return;
      if (!available) {
        setDraftStatus('unavailable');
        return;
      }
      const draft = await store.load();
      if (cancelled) return;
      if (!draft) {
        setDraftStatus('ready');
        return;
      }
      const restored = draft.files.map(
        (file) =>
          new File([file.blob], file.name, { type: file.type, lastModified: file.lastModified }),
      );
      const result = await precheckFiles(restored, caps ? { caps } : {});
      if (cancelled) return;
      filesRef.current = result.accepted;
      setFiles(result.accepted);
      setRulesText(draft.rulesText);
      setUseRules(draft.useRules);
      setWebSearch(draft.webSearch);
      setDraftStatus('restored');
      setNotice(t('form.draft_restored'));
    })();
    return () => {
      cancelled = true;
    };
  }, [store, caps]);

  // Save (debounced) after every user change.
  useEffect(() => {
    if (!store || draftStatus === 'loading' || draftStatus === 'unavailable') return undefined;
    if (!dirty.current) return undefined;
    const handle = window.setTimeout(() => {
      const empty = files.length === 0 && rulesText === '' && useRules && !webSearch;
      if (empty) {
        void store.clear();
        return;
      }
      void store.save({
        rulesText,
        useRules,
        webSearch,
        files: files.map((item) => ({
          name: item.name,
          type: item.file.type,
          lastModified: item.file.lastModified,
          blob: item.file,
        })),
      });
    }, SAVE_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
  }, [store, draftStatus, files, rulesText, useRules, webSearch]);

  const quoteKey = useMemo(
    () => JSON.stringify([files.map((f) => f.id), rulesText, useRules, webSearch, selectedRules]),
    [files, rulesText, useRules, webSearch, selectedRules],
  );
  const quoteWanted = Boolean(quoteProvider) && files.length > 0;
  const quote = quoteWanted && quoteState.key === quoteKey ? quoteState.quote : null;
  const quoteLoading = quoteWanted && quoteState.key !== quoteKey;

  // Quote (debounced, cancellable). State is only written from the async callback.
  useEffect(() => {
    if (!quoteProvider || !quoteWanted) return undefined;
    const controller = new AbortController();
    const handle = window.setTimeout(() => {
      void quoteProvider(
        { files, rulesText, useRules, webSearch, selectedRules },
        controller.signal,
      ).then(
        (result) => {
          if (!controller.signal.aborted) setQuoteState({ key: quoteKey, quote: result });
        },
        () => {
          if (!controller.signal.aborted) setQuoteState({ key: quoteKey, quote: null });
        },
      );
    }, QUOTE_DEBOUNCE_MS);
    return () => {
      controller.abort();
      window.clearTimeout(handle);
    };
  }, [quoteProvider, quoteWanted, quoteKey, files, rulesText, useRules, webSearch, selectedRules]);

  // Paste anywhere outside a text field: files, or text as a .txt file.
  useEffect(() => {
    const onPaste = (event: ClipboardEvent): void => {
      const target = event.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT' || target.isContentEditable)
      ) {
        return;
      }
      const data = event.clipboardData;
      if (!data) return;
      const pasted = Array.from(data.files ?? []);
      if (pasted.length > 0) {
        event.preventDefault();
        void addFiles(pasted);
        return;
      }
      const text = data.getData('text/plain');
      if (text && text.trim() !== '') {
        event.preventDefault();
        pasteCount.current += 1;
        const name = t('dropzone.paste_name', { n: pasteCount.current });
        void addFiles([new File([text], name, { type: 'text/plain', lastModified: Date.now() })]);
        setNotice(t('dropzone.pasted', { name }));
      }
    };
    document.addEventListener('paste', onPaste);
    return () => document.removeEventListener('paste', onPaste);
  }, [addFiles]);

  useEffect(() => {
    const update = (): void => setOnline(navigator.onLine !== false);
    update();
    window.addEventListener('online', update);
    window.addEventListener('offline', update);
    return () => {
      window.removeEventListener('online', update);
      window.removeEventListener('offline', update);
    };
  }, []);

  const onDragEnter = (event: DragEvent): void => {
    if (!dragHasFiles(event)) return;
    event.preventDefault();
    dragDepth.current += 1;
    setDragging(true);
  };
  const onDragOver = (event: DragEvent): void => {
    if (!dragHasFiles(event)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'copy';
  };
  const onDragLeave = (event: DragEvent): void => {
    if (!dragHasFiles(event)) return;
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  };
  const onDrop = (event: DragEvent): void => {
    if (!dragHasFiles(event)) return;
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    void addFiles(Array.from(event.dataTransfer.files));
  };

  const disabledReason = !online
    ? t('form.disabled_offline')
    : files.length === 0
      ? t('form.disabled_no_files')
      : null;

  const discardDraft = async (): Promise<void> => {
    dirty.current = false;
    filesRef.current = [];
    setFiles([]);
    setRejected([]);
    setRulesText('');
    setUseRules(true);
    setWebSearch(false);
    setDraftStatus('ready');
    setNotice(t('form.draft_cleared'));
    await store?.clear();
  };

  const handleSubmit = async (event: SubmitEvent<HTMLFormElement>): Promise<void> => {
    event.preventDefault();
    if (disabledReason || submitting) return;
    const request: RunRequest = {
      files,
      rulesText: useRules ? rulesText : '',
      useRules,
      webSearch,
      selectedRules: useRules ? selectedRules : [],
    };
    if (!onSubmit) {
      setNotice(t('form.submit_unavailable'));
      return;
    }
    setSubmitting(true);
    try {
      await onSubmit(request);
      dirty.current = false;
      await store?.clear();
    } catch (error) {
      setNotice(error instanceof Error && error.message ? error.message : t('errors.unknown'));
    } finally {
      setSubmitting(false);
    }
  };

  const mark = <T,>(setter: (value: T) => void) => {
    return (value: T): void => {
      dirty.current = true;
      setter(value);
    };
  };

  return (
    <form
      className={styles.hero}
      data-compact={compact ? 'true' : undefined}
      data-testid="hero"
      noValidate
      aria-labelledby={`${baseId}-title`}
      onSubmit={(event) => void handleSubmit(event)}
      onDragEnter={onDragEnter}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <h1 id={`${baseId}-title`} className={styles.title}>
        {t('landing.title')}
      </h1>
      <p className={styles.subtitle}>{t('landing.subtitle')}</p>

      <div className={styles.drop}>
        {notice && (
          <div className={styles.notice} role="status">
            <span className={styles.noticeText}>{notice}</span>
            {draftStatus === 'restored' && (
              <button
                type="button"
                className={styles.noticeAction}
                onClick={() => void discardDraft()}
              >
                {t('form.draft_clear')}
              </button>
            )}
            <button
              type="button"
              className={styles.noticeClose}
              aria-label={t('common.close')}
              onClick={() => setNotice(null)}
            >
              <CloseIcon size={16} />
            </button>
          </div>
        )}
        <DropZone
          files={files}
          rejected={rejected}
          dragging={dragging}
          compact={compact}
          disabled={submitting}
          onAdd={(list) => void addFiles(list)}
          onRemove={removeFile}
          onDismissRejection={dismissRejection}
        />
      </div>

      <div className={styles.rules}>
        <RulesBox
          value={rulesText}
          onChange={mark(setRulesText)}
          useRules={useRules}
          onUseRulesChange={mark(setUseRules)}
          webSearch={webSearch}
          onWebSearchChange={mark(setWebSearch)}
          ruleOptions={ruleOptions}
          selectedRules={selectedRules}
          onSelectedRulesChange={mark(setSelectedRules)}
          maxLines={compact ? 3 : 6}
          compact={compact}
          disabled={submitting}
        />
      </div>

      <div className={styles.action}>
        <RunButton
          disabled={Boolean(disabledReason)}
          reason={disabledReason}
          quote={quote}
          quoteLoading={quoteLoading}
          busy={submitting}
        />
      </div>
    </form>
  );
}
