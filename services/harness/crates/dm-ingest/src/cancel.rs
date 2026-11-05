//! Cooperative cancellation of a run.
//!
//! The server reads a run's files on a blocking thread and cannot kill it when the deadline
//! passes; instead it sets a flag that the parsers look at between sources, archive entries,
//! pages and inside their long loops, and stop with a warning. The flag is installed for the
//! current thread with [`scope`]; without one, nothing is ever cancelled.

use std::cell::RefCell;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

thread_local! {
    static FLAG: RefCell<Option<Arc<AtomicBool>>> = const { RefCell::new(None) };
}

/// Runs `f` with `flag` as this thread's cancellation flag (the previous one comes back
/// afterwards, even if `f` panics).
pub fn scope<R>(flag: Arc<AtomicBool>, f: impl FnOnce() -> R) -> R {
    struct Restore(Option<Arc<AtomicBool>>);
    impl Drop for Restore {
        fn drop(&mut self) {
            let previous = self.0.take();
            FLAG.with(|c| *c.borrow_mut() = previous);
        }
    }
    let previous = FLAG.with(|c| c.borrow_mut().replace(flag));
    let _restore = Restore(previous);
    f()
}

/// True once the current thread's run was cancelled.
pub fn cancelled() -> bool {
    FLAG.with(|c| {
        c.borrow()
            .as_ref()
            .is_some_and(|f| f.load(Ordering::Relaxed))
    })
}

/// The warning of a reading that stopped on the deadline.
pub const STOPPED: &str = "leitura interrompida: a execução passou do prazo";

/// A check cheap enough for hot loops: it looks at the flag once every 1024 calls.
#[derive(Default)]
pub(crate) struct Every(u32);

impl Every {
    pub(crate) fn cancelled(&mut self) -> bool {
        self.0 = self.0.wrapping_add(1);
        self.0.is_multiple_of(1024) && cancelled()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scope_installs_and_restores_the_flag() {
        assert!(!cancelled());
        let flag = Arc::new(AtomicBool::new(false));
        scope(flag.clone(), || {
            assert!(!cancelled());
            flag.store(true, Ordering::Relaxed);
            assert!(cancelled());
            let mut every = Every::default();
            assert!((0..2048).any(|_| every.cancelled()));
            let inner = Arc::new(AtomicBool::new(false));
            scope(inner, || assert!(!cancelled()));
            assert!(cancelled());
        });
        assert!(!cancelled());
        let result = std::panic::catch_unwind(|| {
            scope(Arc::new(AtomicBool::new(true)), || panic!("x"));
        });
        assert!(result.is_err());
        assert!(!cancelled(), "a panic does not leave the flag behind");
    }
}
