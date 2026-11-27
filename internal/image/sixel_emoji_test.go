package image

import (
	"image"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSixelEmojiStore_PutGetClear(t *testing.T) {
	s := NewSixelEmojiStore()
	if _, ok := s.Get(7); ok {
		t.Fatal("empty store returned an entry")
	}
	want := SixelEmojiEntry{Bytes: []byte("\x1bPq"), Fallback: []string{"  "}, Cells: image.Pt(2, 1)}
	s.Put(7, want)
	got, ok := s.Get(7)
	if !ok {
		t.Fatal("Put then Get missed")
	}
	if string(got.Bytes) != string(want.Bytes) || len(got.Fallback) != 1 || got.Cells != want.Cells {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
	s.Clear()
	if _, ok := s.Get(7); ok {
		t.Fatal("Clear left the entry behind")
	}
}

// Put replaces: the cell-pixel watcher's re-encode overwrites the old
// payload for the same ID.
func TestSixelEmojiStore_PutReplaces(t *testing.T) {
	s := NewSixelEmojiStore()
	s.Put(3, SixelEmojiEntry{Bytes: []byte("old")})
	s.Put(3, SixelEmojiEntry{Bytes: []byte("new")})
	got, _ := s.Get(3)
	if string(got.Bytes) != "new" {
		t.Fatalf("Get = %q, want %q", got.Bytes, "new")
	}
}

func TestSixelEmojiStore_ConcurrentAccess(t *testing.T) {
	s := NewSixelEmojiStore()
	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		id := uint32(i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.Put(id, SixelEmojiEntry{Bytes: []byte("x")})
		}()
		go func() {
			defer wg.Done()
			s.Get(id)
		}()
	}
	wg.Wait()
}

// The exported helpers must agree with the kitty renderer's encoding:
// the frame resolver decodes the cells without knowing who minted them.
func TestPlaceholderLinesFor_RoundTripsThroughResolver(t *testing.T) {
	id := MintPlaceholderID("E-test", image.Pt(2, 1))
	if id == 0 {
		t.Fatal("registry minted ID 0")
	}
	if again := MintPlaceholderID("E-test", image.Pt(2, 1)); again != id {
		t.Fatalf("re-mint = %d, want the stable %d", again, id)
	}
	lines := PlaceholderLinesFor(id, image.Pt(2, 1))
	if len(lines) != 1 {
		t.Fatalf("lines = %d rows, want 1", len(lines))
	}
	screen := "ab" + lines[0] + "cd"
	out, rects := ResolvePlaceholders(screen, 10, 1, ansi.WcWidth)
	want := []PlaceholderRect{{ID: id, X: 2, Y: 0, Cols: 2, Rows: 1}}
	if len(rects) != 1 || rects[0] != want[0] {
		t.Fatalf("rects = %+v, want %+v", rects, want)
	}
	if strings.ContainsRune(out, PlaceholderRune) {
		t.Fatalf("placeholder left in %q", out)
	}
	if got := ansi.Strip(out); got != "ab  cd" {
		t.Fatalf("stripped frame = %q, want %q", got, "ab  cd")
	}
}

func TestMintPlaceholderID_DistinctKeysDistinctIDs(t *testing.T) {
	a := MintPlaceholderID("E-aaa", image.Pt(2, 1))
	b := MintPlaceholderID("E-bbb", image.Pt(2, 1))
	if a == b {
		t.Fatalf("distinct keys shared ID %d", a)
	}
}
