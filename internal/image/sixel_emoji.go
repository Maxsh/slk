package image

import (
	"image"
	"sync"
)

// This file is the sixel half of emoji-as-images. The emoji path
// (internal/emoji) renders every emoji as kitty-style placeholder cells
// on both kitty-family and sixel terminals; on sixel the frame resolver
// (placeholders.go) finds them at frame exit and the frame painter
// (sixelpaint.go) paints real sixel payloads over the blanked cells.
// The payload travels by ID through this store, because sixel has no
// upload-then-place split: the bytes must be available at paint time.

// SixelEmojiEntry is one emoji's paintable payload: the sixel byte
// stream encoded for Cells, plus the half-block rows to show when the
// emoji is only partly visible (sixel cannot crop in a placement
// command the way a kitty placement crops with x,y,w,h).
type SixelEmojiEntry struct {
	Bytes    []byte
	Fallback []string
	Cells    image.Point
}

// SixelEmojiStore holds the payload of every emoji image ID the emoji
// path minted this session, keyed by the registry ID the placeholder
// cells encode. Written on the UI thread as warm prerenders are
// lifted, read at frame exit when the resolver's rectangles become
// placements; Clear runs from the cell-pixel watcher, so all access is
// mutex-guarded. One store per process, like the registry.
type SixelEmojiStore struct {
	mu      sync.RWMutex
	entries map[uint32]SixelEmojiEntry
}

// NewSixelEmojiStore constructs an empty store.
func NewSixelEmojiStore() *SixelEmojiStore {
	return &SixelEmojiStore{entries: map[uint32]SixelEmojiEntry{}}
}

// Put records the payload for id, replacing any previous one (a
// re-encoded payload after a cell-pixel change).
func (s *SixelEmojiStore) Put(id uint32, e SixelEmojiEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[uint32]SixelEmojiEntry{}
	}
	s.entries[id] = e
}

// Get returns the payload recorded for id.
func (s *SixelEmojiStore) Get(id uint32) (SixelEmojiEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[id]
	return e, ok
}

// Clear drops every payload. Called when the terminal's cell size in
// pixels changed: the payloads are encoded against the old size, and
// the next warm prerender re-encodes lazily.
func (s *SixelEmojiStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = map[uint32]SixelEmojiEntry{}
}

// MintPlaceholderID returns the stable image ID for key at target,
// minting one through the same registry the kitty renderer uses, so
// resolver-decoded IDs are valid no matter which pipeline minted them.
// IDs are process-unique and never reused.
func MintPlaceholderID(key string, target image.Point) uint32 {
	id, _ := registry.Lookup(key, target)
	return id
}

// PlaceholderLinesFor returns the placeholder rows for an image ID at
// a cell footprint: one PlaceholderRune per cell, truecolor foreground
// = ID, diacritics = row and column. The frame resolver decodes them
// back (placeholders.go); the encoding is deliberately the kitty one,
// which is what lets sixel emoji ride the kitty-emulated pipeline.
func PlaceholderLinesFor(id uint32, cells image.Point) []string {
	return buildPlaceholderLines(id, cells)
}
