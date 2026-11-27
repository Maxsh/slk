package ui

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"github.com/gammons/slk/internal/emoji"
	imgpkg "github.com/gammons/slk/internal/image"
)

// This file is the App's half of emoji-as-sixel-images: on a sixel
// terminal with emoji image mode on, every surface draws emoji as
// kitty-style placeholder cells (emoji.Place with ProtoSixel), and the
// frame exit resolves them here into blanked text plus placements the
// frame painter paints with the payloads the emoji path registered in
// the shared store. Same shape as kitty_emulated.go; only the
// placement bytes differ.

// emojiFrameMemo caches the last resolved frame. resolveEmojiFrame
// runs on every View, including the screen-memo hits that hand back
// the identical screen string, so a steady frame must not be parsed
// again.
type emojiFrameMemo struct {
	in, out       string
	width, height int
	graphemeWidth bool
	placements    []imgpkg.SixelPlacement
	valid         bool
	// resolver keeps its cell buffer between frames.
	resolver imgpkg.PlaceholderResolver
}

// SetEmojiSixelStore wires the payload store the emoji PlaceContext
// carries; the frame exit reads it to turn resolver rectangles into
// paintable placements. Nil (kitty, halfblock and off protocols) keeps
// the whole branch off.
func (a *App) SetEmojiSixelStore(s *imgpkg.SixelEmojiStore) {
	a.emojiSixel = s
	a.emojiFrame.valid = false
}

// emojiSixelActive reports whether this App resolves emoji placeholder
// cells itself at frame exit.
func (a *App) emojiSixelActive() bool {
	return a.imgProtocol == imgpkg.ProtoSixel &&
		a.emojiSixel != nil &&
		emoji.ImageModeActive()
}

// resolveEmojiFrame returns content with its emoji placeholder cells
// blanked and the placements that paint them, guards already stamped.
// The returned slice is shared with the memo; the frame store clones
// it on Publish.
func (a *App) resolveEmojiFrame(content string) (string, []imgpkg.SixelPlacement) {
	m := &a.emojiFrame
	if m.valid && m.in == content && m.width == a.width && m.height == a.height &&
		m.graphemeWidth == a.graphemeWidth {
		return m.out, m.placements
	}
	method := ansi.WcWidth
	if a.graphemeWidth {
		method = ansi.GraphemeWidth
	}
	out, rects := m.resolver.Resolve(content, a.width, a.height, method)
	placements := sixelEmojiPlacements(rects, a.emojiSixel)
	stampFrameGuards(out, placements)
	m.in, m.out = content, out
	m.width, m.height = a.width, a.height
	m.graphemeWidth = a.graphemeWidth
	m.placements = placements
	m.valid = true
	return out, placements
}

// sixelEmojiPlacements converts resolver rectangles into painter
// placements. A rectangle whose payload is not registered yet (the
// fetch that carries it is still in flight) is skipped and shows as
// blank cells until the ready message re-renders; so is a rectangle
// that shows only part of its emoji — sixel cannot crop in a placement
// command the way a kitty placement crops with x,y,w,h, so a cut
// emoji's visible cells stay blank (design doc, known limitations).
func sixelEmojiPlacements(rects []imgpkg.PlaceholderRect, store *imgpkg.SixelEmojiStore) []imgpkg.SixelPlacement {
	if len(rects) == 0 {
		return nil
	}
	out := make([]imgpkg.SixelPlacement, 0, len(rects))
	for _, r := range rects {
		entry, ok := store.Get(r.ID)
		if !ok {
			continue
		}
		if r.SrcCol != 0 || r.SrcRow != 0 ||
			r.Cols != entry.Cells.X || r.Rows != entry.Cells.Y {
			continue
		}
		out = append(out, imgpkg.SixelPlacement{
			Key:   fmt.Sprintf("sixel-emoji:%d", r.ID),
			Row:   r.Y + 1, // CUP is 1-based
			Col:   r.X + 1,
			Rows:  r.Rows,
			Cols:  r.Cols,
			Bytes: entry.Bytes,
		})
	}
	return out
}
