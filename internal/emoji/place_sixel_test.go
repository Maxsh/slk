package emoji

import (
	"context"
	goimage "image"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	imgpkg "github.com/gammons/slk/internal/image"
)

// The sixel warm path lifts the prerendered sixel payload into the
// store and returns a placeholder row for the frame resolver: no
// flush, because the payload rides the frame painter, not an APC
// side channel.
func TestPlace_SixelWarmRegistersPayload(t *testing.T) {
	withImageMode(t, 2)
	url, ok := URLForShortcode("orange_book", nil)
	if !ok {
		t.Fatal("precondition: :orange_book: has a URL")
	}
	var captures atomic.Int32
	ff := newFakeFetcher()
	ff.setPrerendered(EmojiCacheKey(url), goimage.Pt(2, 1), imgpkg.Render{
		Lines:    []string{"  "}, // the sixel renderer reserves its footprint in text
		Fallback: []string{"##"},
		OnFlush: func(w io.Writer) error {
			captures.Add(1)
			_, err := w.Write([]byte("SIXELBYTES"))
			return err
		},
	})
	store := imgpkg.NewSixelEmojiStore()
	ctx := PlaceContext{Fetcher: ff, Protocol: imgpkg.ProtoSixel, Sixel: store}

	got, flush, ok := Place(ctx, url, 2)
	if !ok || flush != nil {
		t.Fatalf("Place = (flush=%v, ok=%v), want a flushless placement", flush != nil, ok)
	}
	if strings.Contains(got, " ") {
		t.Fatalf("placement %q is not a placeholder row", got)
	}
	if n := emojiPlacementCells(got); n != 2 {
		t.Fatalf("placement width = %d cells, want 2", n)
	}
	// The store holds the payload under the ID the placeholder encodes.
	id := imgpkg.MintPlaceholderID(EmojiCacheKey(url), goimage.Pt(2, 1))
	e, ok := store.Get(id)
	if !ok {
		t.Fatalf("store has no entry for ID %d", id)
	}
	if string(e.Bytes) != "SIXELBYTES" {
		t.Errorf("payload = %q, want %q", e.Bytes, "SIXELBYTES")
	}
	if len(e.Fallback) != 1 || e.Fallback[0] != "##" {
		t.Errorf("fallback = %v, want the prerender's", e.Fallback)
	}
	if e.Cells != (goimage.Pt(2, 1)) {
		t.Errorf("cells = %v, want (2,1)", e.Cells)
	}
	// A second warm hit reads the store instead of re-capturing.
	if _, _, ok := Place(ctx, url, 2); !ok {
		t.Fatal("second warm Place declined")
	}
	if n := captures.Load(); n != 1 {
		t.Errorf("payload captured %d times, want 1", n)
	}
}

// No store, no placement: the caller falls back to its glyph/text form
// rather than placing an image with no bytes behind it.
func TestPlace_SixelWarmWithoutStoreDeclines(t *testing.T) {
	withImageMode(t, 2)
	url, _ := URLForShortcode("cat", nil)
	t.Cleanup(func() { awaitInflightCleared(EmojiCacheKey(url)) })
	ff := newFakeFetcher()
	ff.setPrerendered(EmojiCacheKey(url), goimage.Pt(2, 1), imgpkg.Render{
		Lines:   []string{"  "},
		OnFlush: func(io.Writer) error { return nil },
	})

	got, flush, ok := Place(PlaceContext{Fetcher: ff, Protocol: imgpkg.ProtoSixel}, url, 2)
	if !ok || got != "  " || flush != nil {
		t.Errorf("Place = (%q, flush=%v, ok=%v), want the cold reservation", got, flush != nil, ok)
	}
}

func TestPlace_SixelColdReservesCells(t *testing.T) {
	withImageMode(t, 2)
	url, _ := URLForShortcode("books", nil)
	t.Cleanup(func() { awaitInflightCleared(EmojiCacheKey(url)) })
	ff := newFakeFetcher()
	ff.fetchFn = func(_ context.Context, _ imgpkg.FetchRequest) (imgpkg.FetchResult, error) {
		return imgpkg.FetchResult{}, nil
	}

	got, flush, ok := Place(PlaceContext{Fetcher: ff, Protocol: imgpkg.ProtoSixel, Sixel: imgpkg.NewSixelEmojiStore()}, url, 2)
	if !ok || got != "  " || flush != nil {
		t.Errorf("cold Place = (%q, flush=%v, ok=%v), want (\"  \", nil, true)", got, flush != nil, ok)
	}
}

// The issue-#50 loop, closed for sixel: once the fetch completes and
// the worker has prerendered, a sixel Place takes the warm path and
// never spawns another fetch.
func TestPlace_SixelNoFetchLoopAfterReady(t *testing.T) {
	withImageMode(t, 2)
	url, _ := URLForShortcode("heart", nil)
	t.Cleanup(func() { awaitInflightCleared(EmojiCacheKey(url)) })
	ff := newFakeFetcher()
	ff.fetchFn = func(_ context.Context, req imgpkg.FetchRequest) (imgpkg.FetchResult, error) {
		// The fetcher's worker prerenders for the active protocol on
		// completion; simulate that here.
		ff.setPrerendered(req.Key, goimage.Pt(2, 1), imgpkg.Render{
			Lines:   []string{"  "},
			OnFlush: func(w io.Writer) error { _, err := w.Write([]byte("P")); return err },
		})
		return imgpkg.FetchResult{}, nil
	}
	ctx := PlaceContext{Fetcher: ff, Protocol: imgpkg.ProtoSixel, Sixel: imgpkg.NewSixelEmojiStore()}

	if got, _, ok := Place(ctx, url, 2); !ok || got != "  " {
		t.Fatalf("first Place = (%q, ok=%v), want the cold reservation", got, ok)
	}
	awaitInflightCleared(EmojiCacheKey(url))
	for i := 0; i < 3; i++ {
		got, flush, ok := Place(ctx, url, 2)
		if !ok || flush != nil || got == "  " {
			t.Fatalf("warm Place %d = (%q, flush=%v, ok=%v), want the placeholder", i, got, flush != nil, ok)
		}
	}
	if n := len(ff.fetchCallsSnapshot()); n != 1 {
		t.Errorf("fetcher called %d times, want exactly 1", n)
	}
}

// emojiPlacementCells reports the display width of a placement the way
// the wrapping code measures it.
func emojiPlacementCells(s string) int {
	return Width(s)
}
