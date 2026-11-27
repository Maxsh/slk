package ui

import (
	goimage "image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/gammons/slk/internal/emoji"
	imgpkg "github.com/gammons/slk/internal/image"
	"github.com/gammons/slk/internal/ui/overlay"
)

// sixelEmojiApp returns a sixel-frame App with emoji image mode on and
// a payload store wired, ready for resolveEmojiFrame.
func sixelEmojiApp(t *testing.T, w, h int) (*App, *imgpkg.SixelEmojiStore) {
	t.Helper()
	emoji.SetImageMode(true, 2)
	t.Cleanup(func() { emoji.SetImageMode(false, 2) })
	a := newTestApp(t, withSize(w, h))
	a.imgProtocol = imgpkg.ProtoSixel
	a.sixelFrames = imgpkg.NewSixelFrameStore()
	store := imgpkg.NewSixelEmojiStore()
	a.SetEmojiSixelStore(store)
	return a, store
}

// emojiCellsPt is the emoji footprint every Place call uses here.
var emojiCellsPt = goimage.Pt(2, 1)

// registerEmoji places one emoji's payload in the store under the ID
// its placeholder cells encode, and returns the placeholder row.
func registerEmoji(t *testing.T, store *imgpkg.SixelEmojiStore, key string, payload string) string {
	t.Helper()
	id := imgpkg.MintPlaceholderID(key, emojiCellsPt)
	store.Put(id, imgpkg.SixelEmojiEntry{
		Bytes:    []byte(payload),
		Fallback: []string{"##"},
		Cells:    emojiCellsPt,
	})
	lines := imgpkg.PlaceholderLinesFor(id, emojiCellsPt)
	return lines[0]
}

// A frame with an emoji in running text resolves to blanked text plus
// one placement carrying the payload.
func TestResolveEmojiFrame_BlanksAndPlaces(t *testing.T) {
	a, store := sixelEmojiApp(t, 20, 2)
	pill := registerEmoji(t, store, "E-heart", "SIXEL-heart")
	content := "ab" + pill + "cd"
	out, placements := a.resolveEmojiFrame(content)
	if got := ansi.Strip(strings.ReplaceAll(out, "\n", "")); !strings.Contains(got, "ab  cd") {
		t.Fatalf("resolved content %q does not blank the placeholder", out)
	}
	if strings.ContainsRune(out, imgpkg.PlaceholderRune) {
		t.Fatalf("placeholder rune left in %q", out)
	}
	if len(placements) != 1 {
		t.Fatalf("placements = %+v, want one", placements)
	}
	p := placements[0]
	if string(p.Bytes) != "SIXEL-heart" {
		t.Fatalf("placement bytes = %q, want the store payload", p.Bytes)
	}
	if p.Row != 1 || p.Col != 3 || p.Rows != 1 || p.Cols != 2 {
		t.Fatalf("placement slot = %d,%d %dx%d, want row 1 col 3 2x1", p.Row, p.Col, p.Rows, p.Cols)
	}
	if p.Guard == "" {
		t.Fatal("placement has no guard")
	}
	if p.Erase != nil {
		t.Fatal("sixel emoji placement carries an erase command; ECH is correct for sixel")
	}
}

// Two identical emoji side by side share an ID; the resolver's column
// restart splits them into two rectangles and the painter paints both
// slots.
func TestResolveEmojiFrame_AdjacentIdenticalAreTwoPlacements(t *testing.T) {
	a, store := sixelEmojiApp(t, 20, 1)
	pill := registerEmoji(t, store, "E-heart", "SIXEL-heart")
	_, placements := a.resolveEmojiFrame(pill + pill)
	if len(placements) != 2 {
		t.Fatalf("placements = %+v, want two slots", placements)
	}
	if placements[0].Col != 1 || placements[1].Col != 3 {
		t.Fatalf("slots = %d and %d, want cols 1 and 3", placements[0].Col, placements[1].Col)
	}
}

// A rectangle whose payload is not in the store yet (its fetch is in
// flight) is skipped, not placed with empty bytes.
func TestResolveEmojiFrame_MissingPayloadSkipped(t *testing.T) {
	a, _ := sixelEmojiApp(t, 20, 1)
	id := imgpkg.MintPlaceholderID("E-unregistered", emojiCellsPt)
	lines := imgpkg.PlaceholderLinesFor(id, emojiCellsPt)
	_, placements := a.resolveEmojiFrame("x" + lines[0])
	if len(placements) != 0 {
		t.Fatalf("placements = %+v, want none for an unregistered payload", placements)
	}
}

// A rectangle showing only part of its emoji (cut by truncation) is
// skipped: sixel cannot crop in a placement command.
func TestResolveEmojiFrame_PartialRectSkipped(t *testing.T) {
	a, store := sixelEmojiApp(t, 20, 1)
	registerEmoji(t, store, "E-cut", "SIXEL-cut")
	lines := imgpkg.PlaceholderLinesFor(imgpkg.MintPlaceholderID("E-cut", emojiCellsPt), emojiCellsPt)
	cut := cutPlaceholderRow(lines[0], 1)
	_, placements := a.resolveEmojiFrame(cut)
	if len(placements) != 0 {
		t.Fatalf("placements = %+v, want none for a partial rect", placements)
	}
}

// cutPlaceholderRow keeps the first keep placeholder cells of a
// placeholder row. A cell is the placeholder rune plus its two
// combining diacritics; everything else (the SGR prefix, the reset)
// is copied through.
func cutPlaceholderRow(row string, keep int) string {
	r := []rune(row)
	var out []rune
	cells := 0
	for i := 0; i < len(r); {
		if r[i] == imgpkg.PlaceholderRune {
			if cells >= keep {
				break
			}
			out = append(out, r[i], r[i+1], r[i+2])
			cells++
			i += 3
			continue
		}
		out = append(out, r[i])
		i++
	}
	return string(out) + "\x1b[39m"
}

// finalizeSixelView under sixel+emoji: content blanked, pane
// placements kept with guards stamped against the blanked frame, and
// the emoji placements appended after them.
func TestFinalizeSixelView_MergesPaneAndEmojiPlacements(t *testing.T) {
	a, store := sixelEmojiApp(t, 20, 2)
	pill := registerEmoji(t, store, "E-heart", "SIXEL-heart")
	content := "ab" + pill + "cd"
	pane := imgpkg.SixelPlacement{Key: "pane-1", Row: 1, Col: 1, Rows: 1, Cols: 1, Bytes: []byte("PANE")}

	v := a.finalizeSixelView(tea.NewView(content), []imgpkg.SixelPlacement{pane})

	if strings.ContainsRune(v.Content, imgpkg.PlaceholderRune) {
		t.Fatalf("published content keeps the placeholder: %q", v.Content)
	}
	frame, ok := a.sixelFrames.Take(frameIDFromTitle(t, v.WindowTitle))
	if !ok {
		t.Fatal("no frame published")
	}
	foundPane, foundEmoji := false, false
	for _, p := range frame.Placements {
		switch {
		case p.Key == "pane-1":
			foundPane = true
			if p.Guard == "" {
				t.Fatal("pane placement has no guard")
			}
		case strings.HasPrefix(p.Key, "sixel-emoji:"):
			foundEmoji = true
		}
	}
	if !foundPane || !foundEmoji {
		t.Fatalf("published placements = %+v, want the pane's and the emoji's", frame.Placements)
	}
}

// A steady frame is not resolved again (the screen memo hands back the
// same string): the memo's own placements slice comes back.
func TestResolveEmojiFrame_MemoHits(t *testing.T) {
	a, store := sixelEmojiApp(t, 20, 1)
	pill := registerEmoji(t, store, "E-heart", "SIXEL-heart")
	content := "ab" + pill
	a.resolveEmojiFrame(content)
	if !a.emojiFrame.valid || a.emojiFrame.in != content {
		t.Fatal("memo not seeded")
	}
	_, again := a.resolveEmojiFrame(content)
	if &again[0] != &a.emojiFrame.placements[0] {
		t.Fatal("second resolve rebuilt the placements")
	}
}

// Under a modal, the backdrop's emoji placeholders are blanked by
// DimmedOverlay and only the modal's own resolve — the reaction
// picker's emoji survive, and the withholds-all-sixel rule does not
// apply.
func TestFinalizeSixelView_EmojiUnderModal(t *testing.T) {
	const w, h = 30, 5
	a, store := sixelEmojiApp(t, w, h)
	bg := registerEmoji(t, store, "E-bg", "SIXEL-bg")
	box := "[" + registerEmoji(t, store, "E-picker", "SIXEL-picker") + "]"
	var backdrop strings.Builder
	for y := 0; y < h; y++ {
		backdrop.WriteString(bg + strings.Repeat(" ", w-2) + "\n")
	}
	screen := overlay.DimmedOverlay(w, h, strings.TrimSuffix(backdrop.String(), "\n"), box, 0.5)

	_, placements := a.resolveEmojiFrame(screen)
	if len(placements) != 1 {
		t.Fatalf("placements = %+v, want only the modal's emoji", placements)
	}
	if string(placements[0].Bytes) != "SIXEL-picker" {
		t.Fatalf("placement bytes = %q, want the picker's payload", placements[0].Bytes)
	}
}
