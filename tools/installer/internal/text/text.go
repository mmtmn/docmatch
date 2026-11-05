// Package text serves the installer's user-facing strings: the `installer` section of
// packages/locales/pt-BR.json (embedded through internal/assets). Keys are dot paths under that
// section ("mode.title"); `{name}` placeholders are filled from key/value pairs.
package text

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/mmtmn/docmatch/tools/installer/internal/assets"
)

var (
	once    sync.Once
	strs    map[string]string
	lists   map[string][]string
	loadErr error
)

func load() {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(assets.LocalesPTBR, &root); err != nil {
		loadErr = err
		return
	}
	raw, ok := root["installer"]
	if !ok {
		loadErr = fmt.Errorf("text: seção installer ausente em pt-BR.json")
		return
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		loadErr = err
		return
	}
	strs = map[string]string{}
	lists = map[string][]string{}
	flatten("", tree)
}

func flatten(prefix string, node map[string]any) {
	for k, v := range node {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case string:
			strs[key] = val
		case []any:
			items := make([]string, 0, len(val))
			for _, item := range val {
				if s, ok := item.(string); ok {
					items = append(items, s)
				}
			}
			lists[key] = items
		case map[string]any:
			flatten(key, val)
		}
	}
}

func fill(s string, args []any) string {
	for i := 0; i+1 < len(args); i += 2 {
		s = strings.ReplaceAll(s, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return s
}

// T returns the string at key with `{name}` placeholders filled from args (name, value, ...).
// A missing key renders as the key itself in brackets, so it is visible and caught by the tests.
func T(key string, args ...any) string {
	once.Do(load)
	s, ok := strs[key]
	if !ok {
		return "[" + key + "]"
	}
	return fill(s, args)
}

// L returns the list at key, each item filled from args.
func L(key string, args ...any) []string {
	once.Do(load)
	items := lists[key]
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = fill(s, args)
	}
	return out
}

// Has reports whether key exists (string or list).
func Has(key string) bool {
	once.Do(load)
	_, s := strs[key]
	_, l := lists[key]
	return s || l
}

// Keys lists every key of the section, sorted.
func Keys() []string {
	once.Do(load)
	out := make([]string, 0, len(strs)+len(lists))
	for k := range strs {
		out = append(out, k)
	}
	for k := range lists {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Err reports a problem loading the section (nil when it loaded).
func Err() error {
	once.Do(load)
	return loadErr
}
