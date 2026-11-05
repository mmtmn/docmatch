//! html5ever under limits: the markup is fed to the parser in small chunks and the tree it
//! builds is measured between them, so a page stops being read where its tree gets too deep
//! or too large.
//!
//! The tree builder looks up its stack of open elements for most tokens and re-creates the
//! open formatting elements (`<b>`, `<font>`...) before text, so a page nested a million levels
//! deep costs it time quadratic in the depth, and a few hundred formatting elements re-opened
//! before every word cost it memory quadratic in the input. Estimating that from the markup
//! means re-implementing the tree builder (implied and ignored end tags, the adoption agency,
//! foster parenting, templates); measuring the tree it actually builds does not: unmatched end
//! tags, comment forms and implied elements are whatever html5ever makes of them. Between two
//! chunks of [`CHUNK`] bytes the tree grows by a bounded amount, so the stack stays within
//! [`MAX_DEPTH`] plus one chunk of elements and every token costs bounded time: parsing is
//! linear in the input. Splitting the input changes nothing in the tree (the tokenizer
//! resumes where it stopped).

use html5ever::tendril::{StrTendril, TendrilSink};
use html5ever::ParseOpts;
use scraper::{Html, HtmlTreeSink};

use crate::cancel::Every;

/// Depth of the tree read (ancestors of its deepest node, the document included).
pub(crate) const MAX_DEPTH: usize = 512;
/// Nodes of the tree read: elements (written, implied or re-opened), text and comments.
pub(crate) const MAX_NODES: usize = 400_000;
/// Bytes of markup between two measures of the tree.
const CHUNK: usize = 256;

/// Why a page was not read to the end.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Limit {
    /// The tree got deeper than [`MAX_DEPTH`].
    Depth,
    /// The tree got more than [`MAX_NODES`] nodes.
    Size,
    /// The run was cancelled.
    Cancelled,
}

/// The end of the chunk that starts at `at`: [`CHUNK`] bytes on a character boundary, at
/// least one character.
fn chunk_end(s: &str, at: usize) -> usize {
    let mut end = (at + CHUNK).min(s.len());
    while !s.is_char_boundary(end) {
        end += 1;
    }
    end
}

/// The depth of the node created last (the insertion point of the tree builder, give or
/// take the elements closed since) and the number of nodes.
fn measure(html: &Html) -> (usize, usize) {
    let mut nodes = html.tree.nodes();
    let count = nodes.len();
    let depth = nodes.next_back().map_or(0, |n| n.ancestors().count());
    (depth, count)
}

/// Parses `markup` as a document, stopping at the first chunk boundary where the tree is
/// past a limit; returns the tree (closed as at the end of the input) and the limit hit.
pub(crate) fn parse_bounded(markup: &str) -> (Html, Option<Limit>) {
    parse_bounded_with(markup, MAX_DEPTH, MAX_NODES)
}

fn parse_bounded_with(markup: &str, max_depth: usize, max_nodes: usize) -> (Html, Option<Limit>) {
    let mut parser = html5ever::parse_document(
        HtmlTreeSink::new(Html::new_document()),
        ParseOpts::default(),
    );
    let mut every = Every::default();
    let mut at = 0usize;
    let mut limit = None;
    while at < markup.len() {
        if every.cancelled() {
            limit = Some(Limit::Cancelled);
            break;
        }
        let end = chunk_end(markup, at);
        parser.process(StrTendril::from(&markup[at..end]));
        at = end;
        let (depth, nodes) = measure(&parser.tokenizer.sink.sink.0.borrow());
        if depth > max_depth {
            limit = Some(Limit::Depth);
            break;
        }
        if nodes > max_nodes {
            limit = Some(Limit::Size);
            break;
        }
    }
    (parser.finish(), limit)
}

#[cfg(test)]
mod tests {
    use super::*;
    use proptest::prelude::*;

    fn deepest(html: &Html) -> usize {
        html.tree
            .nodes()
            .map(|n| n.ancestors().count())
            .max()
            .unwrap_or(0)
    }

    fn limit(markup: &str) -> Option<Limit> {
        parse_bounded(markup).1
    }

    #[test]
    fn deep_trees_stop_near_the_limit_whatever_the_markup() {
        for unit in [
            "<div>",
            "<span></i></div></td>",
            "<table><td>",
            "<ul><li>",
            "<div/>",
            "<b><div></b>",
            "<form><div></form>",
            "<svg><g>",
            "<span a\"><div>\">",
        ] {
            let page = unit.repeat(5_000);
            let (html, hit) = parse_bounded(&page);
            assert_eq!(hit, Some(Limit::Depth), "{unit}");
            let depth = deepest(&html);
            assert!(depth <= MAX_DEPTH + CHUNK, "{unit}: {depth}");
        }
    }

    #[test]
    fn comments_end_where_the_tokenizer_ends_them() {
        let divs = "<div>".repeat(MAX_DEPTH + 10);
        for open in [
            "<!-->",
            "<!--->",
            "<!-- a --!>",
            "<!-- a --->",
            "<?php x ?>",
            "</ x>",
        ] {
            assert_eq!(
                limit(&format!("{open}{divs}")),
                Some(Limit::Depth),
                "{open}"
            );
        }
        assert_eq!(limit(&format!("<!-- {divs} -->ok")), None);
        assert_eq!(limit(&format!("<script>{divs}</script>ok")), None);
        assert_eq!(limit(&format!("<span title=\"{divs}\">ok</span>")), None);
    }

    #[test]
    fn flat_pages_are_read_whole() {
        for unit in [
            "<p>a",
            "<li>a",
            "<span>x</span>",
            "<tr><td><font face=x>1<td>2",
        ] {
            let page = format!("<table>{}</table>", unit.repeat(5_000));
            let (html, hit) = parse_bounded(&page);
            assert_eq!(hit, None, "{unit}");
            assert!(deepest(&html) < 16, "{unit}: {}", deepest(&html));
        }
    }

    #[test]
    fn total_nodes_are_bounded() {
        // A small budget: the mechanism is the same at the real one (timed in release by
        // tests/bounded_memory.rs).
        const NODES: usize = 20_000;
        for unit in ["<br>", "<!---->", "<i>x</i>", "x<br>"] {
            let (html, hit) = parse_bounded_with(&unit.repeat(NODES), MAX_DEPTH, NODES);
            assert_eq!(hit, Some(Limit::Size), "{unit}");
            assert!(html.tree.nodes().len() <= NODES + 2 * CHUNK, "{unit}");
        }
        // Formatting elements re-opened before every word: 100 elements per `</p><p>x`.
        let mut page = String::from("<p>");
        for k in 0..100 {
            page.push_str(&format!("<b id={k}>"));
        }
        page.push_str(&"</p><p>x".repeat(5_000));
        let (html, hit) = parse_bounded_with(&page, MAX_DEPTH, NODES);
        assert_eq!(hit, Some(Limit::Size));
        assert!(html.tree.nodes().len() <= NODES + 40 * 100);
    }

    fn atom() -> impl Strategy<Value = &'static str> {
        prop::sample::select(vec![
            "<div>",
            "</div>",
            "<p>",
            "</p>",
            "<b>",
            "</b>",
            "<i class=x>",
            "</i>",
            "<font>",
            "<a href=#>",
            "</a>",
            "<table>",
            "</table>",
            "<tr>",
            "</tr>",
            "<td>",
            "</td>",
            "<li>",
            "<ul>",
            "</ul>",
            "<span>",
            "</span>",
            "x",
            " ",
            "<!-- c -->",
            "<!-->",
            "<br>",
            "<h1>",
            "</h2>",
            "<select>",
            "<option>",
            "<svg>",
            "</svg>",
            "<path/>",
            "é",
            "<form>",
            "</form>",
            "<template>",
            "</template>",
            "<script>",
            "</script>",
            "<textarea>",
            "</textarea>",
            "<!",
            "<",
            ">",
            "&amp;",
            "<plaintext>",
        ])
    }

    proptest! {
        #![proptest_config(ProptestConfig::with_cases(300))]

        /// Feeding the parser in chunks builds the tree that one call builds.
        #[test]
        fn chunks_do_not_change_the_tree(atoms in prop::collection::vec(atom(), 0..300)) {
            let markup = atoms.concat();
            let (html, hit) = parse_bounded(&markup);
            prop_assert_eq!(hit, None);
            prop_assert_eq!(html.html(), Html::parse_document(&markup).html());
        }
    }
}
