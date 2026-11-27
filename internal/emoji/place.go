package emoji

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	goimage "image"
	"io"
	"strings"
	"sync"

	imgpkg "github.com/gammons/slk/internal/image"
)

// PlaceFetcher is the subset of *image.Fetcher's API that Place uses.
// Defined as an interface so tests can substitute a fake without
// constructing a full Fetcher + HTTP server.
type PlaceFetcher interface {
	Fetch(ctx context.Context, req imgpkg.FetchRequest) (imgpkg.FetchResult, error)
	Prerendered(key string, cellTarget goimage.Point, proto imgpkg.Protocol) (imgpkg.Render, bool)
}

// PlaceContext bundles the dependencies Place needs. Built once per
// app instance and passed to every UI surface that renders emoji.
type PlaceContext struct {
	// Fetcher downloads + decodes + (via ConfigurePrerender) pre-encodes
	// the kitty payload. Required.
	Fetcher PlaceFetcher

	// SendMsg dispatches an EmojiImageReadyMsg when a cold-cache fetch
	// completes. Typically wraps bubbletea Program.Send. If nil, the
	// fetch still runs but no re-render is signaled — useful in tests.
	SendMsg func(msg any)

	// Protocol selects how a warm placement reaches the terminal.
	// ProtoKitty (also the zero-context default: every existing caller
	// and test builds a PlaceContext without this field) uploads the
	// image through kitty APC escapes fired into imgpkg.KittyOutput.
	// ProtoSixel registers the paintable payload in Sixel and returns a
	// placeholder row the App's frame resolver turns into a painter
	// placement — no side channel, the payload rides the frame.
	// ProtoOff normalizes to ProtoKitty here (Place is gated on image
	// mode upstream, which is only on for kitty and sixel).
	Protocol imgpkg.Protocol

	// Sixel is the payload store for the ProtoSixel path, shared with
	// the App's frame painter. Nil declines the sixel warm path, so the
	// caller falls back to its glyph/text form rather than placing an
	// image with no bytes behind it.
	Sixel *imgpkg.SixelEmojiStore
}

// EmojiImageReadyMsg is dispatched via PlaceContext.SendMsg when a
// previously-cold emoji image has finished fetching and is now
// renderable from the warm path. UI surfaces that buffer emoji
// placements should invalidate their render caches for any entry
// referencing this URL.
//
// Unlike BlockImageReadyMsg (per-message), EmojiImageReadyMsg is
// global — the same emoji can appear in any message, the picker, the
// autocomplete dropdown, etc. Reducers should treat it as a
// coarse-grained invalidation signal across all surfaces that render
// emoji.
type EmojiImageReadyMsg struct {
	URL string
}

// inflightEmoji guards against firing multiple fetch goroutines for
// the same URL when the cold-cache branch is reached concurrently
// from multiple UI surfaces (e.g., the same :thumbsup: appearing in
// 10 messages on the same View() pass).
var (
	inflightEmoji   = map[string]struct{}{}
	inflightEmojiMu sync.Mutex
)

// negativeEmojiCache records URLs whose fetch has failed at least once
// in this process. Subsequent Place() calls for these URLs return
// ok=false so callers fall back to legacy glyph/text rendering instead
// of leaving a blank cold-cache reservation forever.
//
// This addresses kyokomi-codemap entries that don't correspond to any
// Slack emoji (e.g., :heart_suit:, :diamond_suit:) — the URL builder
// produces a syntactically-valid URL that 404s on Slack's CDN. Without
// negative caching, these rows would show blank cells indefinitely.
//
// Lifetime: process-scoped. No TTL, no disk persistence. A slk restart
// picks up emoji that Slack may have added since.
var (
	negativeEmojiCache   = map[string]struct{}{}
	negativeEmojiCacheMu sync.RWMutex
)

// markEmojiURLFailed records url as a known-failing emoji fetch so
// future Place() calls for it short-circuit to the legacy fallback.
// Called from spawnEmojiFetch's goroutine on any fetch error.
func markEmojiURLFailed(url string) {
	negativeEmojiCacheMu.Lock()
	negativeEmojiCache[url] = struct{}{}
	negativeEmojiCacheMu.Unlock()
}

// isEmojiURLFailed reports whether url has been marked failed via
// markEmojiURLFailed. Used by Place() as a short-circuit.
func isEmojiURLFailed(url string) bool {
	negativeEmojiCacheMu.RLock()
	_, failed := negativeEmojiCache[url]
	negativeEmojiCacheMu.RUnlock()
	return failed
}

// resetNegativeEmojiCache clears the negative cache. Test-only.
func resetNegativeEmojiCache() {
	negativeEmojiCacheMu.Lock()
	negativeEmojiCache = map[string]struct{}{}
	negativeEmojiCacheMu.Unlock()
}

// EmojiCacheKey returns the cache key used by Place for url. Stable
// hash of the URL with an "E-" prefix to isolate emoji entries from
// avatars, attachments, and block-kit images in the shared disk cache.
//
// Exported so reducers can correlate EmojiImageReadyMsg URLs to cache
// entries when wiring re-render invalidation.
func EmojiCacheKey(url string) string {
	sum := sha1.Sum([]byte(url))
	return "E-" + hex.EncodeToString(sum[:8])
}

// Place returns the placement string + optional flush callback
// for url at the given cell footprint (cells wide x 1 row tall).
//
// Returns ("", nil, false) when:
//   - url is empty
//   - ctx.Fetcher is nil
//   - cells < 1
//   - the warm path is a sixel hit whose payload cannot be lifted
//     (no store, no flush, or a flush error)
//
// Warm path (image already fetched + prerendered for this cells target):
// returns (placement, flush, true). Under ProtoKitty the placement is
// kitty's own placeholder row and flush MUST be invoked once per frame
// to upload the kitty payload (idempotent via the registry — multiple
// invocations are safe and the first wins). Under ProtoSixel the
// placement is a kitty-style placeholder row the frame resolver decodes,
// the payload is registered in ctx.Sixel keyed by the minted image ID,
// and flush is nil — the painter paints the bytes with the frame.
//
// Cold path: returns (strings.Repeat(" ", cells), nil, true). Spawns
// an async fetch (deduplicated per URL across all callers) and, on
// completion, dispatches EmojiImageReadyMsg{URL: url} via
// ctx.SendMsg.
func Place(ctx PlaceContext, url string, cells int) (string, func(io.Writer) error, bool) {
	if url == "" || ctx.Fetcher == nil || cells < 1 {
		return "", nil, false
	}

	// Negative-cache short-circuit. If this URL's fetch has failed earlier
	// in the process (e.g., kyokomi-codemap entry without a real Slack
	// asset), bail out so the caller falls back to the legacy glyph/text
	// rendering instead of showing a blank cold-cache reservation forever.
	if isEmojiURLFailed(url) {
		return "", nil, false
	}

	proto := ctx.Protocol
	if proto == imgpkg.ProtoOff {
		// The zero PlaceContext means the kitty behavior Place has
		// always had; an explicit ProtoOff is unreachable because
		// callers gate on image mode before calling.
		proto = imgpkg.ProtoKitty
	}
	// Custom-only scope: a standard emoji's glyph costs nothing and
	// (on a sixel terminal) its image would cost kilobytes per
	// repaint, so decline and let the caller draw its glyph. Standard
	// assets are exactly the ones this package builds URLs for;
	// workspace customs live on emoji.slack-edge.com, and a custom
	// alias to a standard emoji correctly counts as standard.
	if ImageScope() == ScopeCustom && strings.HasPrefix(url, CDNBaseURL) {
		return "", nil, false
	}
	target := goimage.Pt(cells, 1)
	key := EmojiCacheKey(url)

	// Warm path: a prerendered placement is ready in the fetcher's
	// prerender memo, under the emoji render protocol. The fetcher's
	// worker populated this off the UI thread when the fetch completed.
	// A sixel memo entry carries space lines (the sixel renderer
	// reserves its footprint in text), so the placement comes from the
	// minted ID's placeholder row instead, and the payload is lifted
	// into the store for the frame painter.
	if r, ok := ctx.Fetcher.Prerendered(key, target, proto); ok {
		if proto == imgpkg.ProtoSixel {
			if placement, ok := placeSixelWarm(ctx, key, target, r); ok {
				return placement, nil, true
			}
		} else if len(r.Lines) > 0 {
			return r.Lines[0], r.OnFlush, true
		}
	}

	// Cold path: kick off an async fetch (deduplicated per-URL across
	// all concurrent Place calls), return a cells-wide space
	// reservation. Width math reports the same value (cells) for both
	// the warm and cold output, so the eventual swap doesn't shift
	// layout.
	spawnEmojiFetch(ctx, key, url, target)
	return strings.Repeat(" ", cells), nil, true
}

// placeSixelWarm lifts a warm prerendered sixel Render into ctx.Sixel
// under a minted image ID and returns the placeholder row the App's
// frame resolver decodes. Emoji are one row tall, so the placement is
// the single row. ok is false when there is no store, no payload
// flush, the flush errors, or no placeholder row came back — the
// caller then takes the cold path rather than placing bytes it cannot
// paint.
func placeSixelWarm(ctx PlaceContext, key string, target goimage.Point, r imgpkg.Render) (string, bool) {
	if ctx.Sixel == nil || r.OnFlush == nil {
		return "", false
	}
	id := imgpkg.MintPlaceholderID(key, target)
	if _, ok := ctx.Sixel.Get(id); !ok {
		var payload bytes.Buffer
		if err := r.OnFlush(&payload); err != nil {
			return "", false
		}
		ctx.Sixel.Put(id, imgpkg.SixelEmojiEntry{
			Bytes:    payload.Bytes(),
			Fallback: r.Fallback,
			Cells:    target,
		})
	}
	lines := imgpkg.PlaceholderLinesFor(id, target)
	if len(lines) == 0 {
		return "", false
	}
	return lines[0], true
}

// PlaceShortcode places the emoji a Slack shortcode names (no colons,
// e.g. "orange_book", a workspace custom emoji, an alias, a skin-toned
// name) as an image: URLForShortcode, then Place. It returns what Place
// returns.
//
// ok is false when image mode is off, ctx has no fetcher, the name has
// no URL, or Place declines (including a URL whose fetch failed
// before), so the caller falls back to its glyph or text form.
func PlaceShortcode(ctx PlaceContext, name string, customs map[string]string, cells int) (string, func(io.Writer) error, bool) {
	if !ImageModeActive() || ctx.Fetcher == nil || name == "" {
		return "", nil, false
	}
	url, ok := URLForShortcode(name, customs)
	if !ok {
		return "", nil, false
	}
	return Place(ctx, url, cells)
}

// spawnEmojiFetch starts (at most) one fetch goroutine per URL. On
// success, dispatches EmojiImageReadyMsg via ctx.SendMsg so the host
// can re-render. The fetch's CellTarget triggers the fetcher's
// prerender machinery (see Fetcher.maybePrerender) so subsequent
// Place calls hit the warm path without further work on the UI
// thread.
func spawnEmojiFetch(ctx PlaceContext, key, url string, target goimage.Point) {
	inflightEmojiMu.Lock()
	if _, busy := inflightEmoji[key]; busy {
		inflightEmojiMu.Unlock()
		return
	}
	inflightEmoji[key] = struct{}{}
	inflightEmojiMu.Unlock()

	send := ctx.SendMsg
	fetcher := ctx.Fetcher

	go func() {
		defer func() {
			inflightEmojiMu.Lock()
			delete(inflightEmoji, key)
			inflightEmojiMu.Unlock()
		}()

		_, err := fetcher.Fetch(context.Background(), imgpkg.FetchRequest{
			Key:        key,
			URL:        url,
			CellTarget: target, // triggers prerender into the kitty pipeline
			// Target left zero: Slack emoji PNGs are already ~64px;
			// the kitty prerender downscales to the cell-pixel target
			// internally during RenderKey.
		})
		if err != nil {
			// Mark this URL as known-failing so subsequent Place() calls
			// short-circuit to the legacy fallback. Common cases: kyokomi
			// codemap entries that don't correspond to a real Slack emoji
			// (404), the rare transient network error (also negative-cached
			// for the rest of this session; a restart picks it up fresh).
			// Fetcher logs the error in its own [imgfetch] surface; no
			// need to duplicate here.
			markEmojiURLFailed(url)
			// Dispatch the same ready msg as success so the UI re-renders
			// and the negative cache takes effect on next View(). Semantically
			// "this URL's status is now resolved (to fallback)".
			if send != nil {
				send(EmojiImageReadyMsg{URL: url})
			}
			return
		}
		if send != nil {
			send(EmojiImageReadyMsg{URL: url})
		}
	}()
}
