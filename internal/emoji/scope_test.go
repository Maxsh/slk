package emoji

import (
	"context"
	goimage "image"
	"io"
	"testing"

	imgpkg "github.com/gammons/slk/internal/image"
)

// Under the custom-only scope, a standard emoji's CDN URL declines (the
// caller draws its glyph) while a workspace custom emoji still places.
func TestPlace_ScopeCustom_ImagesCustomsOnly(t *testing.T) {
	withImageMode(t, 2)
	t.Cleanup(func() { SetImageScope(ScopeAll) })
	SetImageScope(ScopeCustom)
	ff := newFakeFetcher()

	stdURL, ok := URLForShortcode("heart", nil)
	if !ok {
		t.Fatal("precondition: :heart: resolves")
	}
	if s, flush, ok := Place(PlaceContext{Fetcher: ff}, stdURL, 2); ok || s != "" || flush != nil {
		t.Errorf("standard emoji under ScopeCustom = (%q, flush=%v, ok=%v), want declined", s, flush != nil, ok)
	}

	customURL := "https://emoji.slack-edge.com/T1/partyparrot/abc.gif"
	ff.setPrerendered(EmojiCacheKey(customURL), goimage.Pt(2, 1), imgpkg.Render{Lines: []string{"<parrot>"}})
	if s, _, ok := Place(PlaceContext{Fetcher: ff}, customURL, 2); !ok || s != "<parrot>" {
		t.Errorf("custom emoji under ScopeCustom = (%q, ok=%v), want placed", s, ok)
	}

	// A custom alias to a standard emoji is a standard emoji: declined.
	aliasURL, ok := BuildCustomEmojiURL("parrot_std", map[string]string{"parrot_std": "alias:heart"})
	if !ok {
		t.Fatal("precondition: alias resolves")
	}
	if aliasURL != stdURL {
		t.Fatalf("alias URL %q, want the standard asset %q", aliasURL, stdURL)
	}
	if _, _, ok := Place(PlaceContext{Fetcher: ff}, aliasURL, 2); ok {
		t.Error("alias-to-standard under ScopeCustom placed, want declined")
	}
}

// The zero scope is ScopeAll: every existing PlaceContext behaves as
// before.
func TestImageScope_ZeroIsAll(t *testing.T) {
	withImageMode(t, 2)
	if ImageScope() != ScopeAll {
		t.Fatalf("default scope = %v, want all", ImageScope())
	}
}

// Under the custom scope the width math is lipgloss's: the string holds
// glyphs and placeholder cells, both of which lipgloss measures as the
// terminal draws them.
func TestWidth_ScopeCustomDelegatesToLipgloss(t *testing.T) {
	withImageMode(t, 2)
	t.Cleanup(func() { SetImageScope(ScopeAll) })
	SetImageScope(ScopeCustom)
	if got := Width("a\U0001F525b"); got != 4 {
		t.Errorf("Width(fire between letters) = %d, want lipgloss's 4", got)
	}
}

// The sixel cold path never runs for a declined standard URL, so no
// fetch is spawned for it.
func TestPlace_ScopeCustom_DoesNotFetchStandard(t *testing.T) {
	withImageMode(t, 2)
	t.Cleanup(func() { SetImageScope(ScopeAll) })
	SetImageScope(ScopeCustom)
	ff := newFakeFetcher()
	stdURL, _ := URLForShortcode("smile", nil)
	_, _, _ = Place(PlaceContext{Fetcher: ff}, stdURL, 2)
	if n := len(ff.fetchCallsSnapshot()); n != 0 {
		t.Errorf("fetcher called %d times for a declined URL, want 0", n)
	}
	_ = io.Discard
	_ = context.Background
}
