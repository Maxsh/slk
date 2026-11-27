package emoji

import "sync"

// Image-mode is a process-global flag that records whether emoji
// should be rendered as PNG images via the kitty graphics protocol.
// When active, the width math, render, and shouldn't-probe decisions
// all branch off this single value.
//
// Set once at startup (cmd/slk/main.go) after the user's config has
// been loaded and the terminal's image protocol has been detected.
// Not safe to toggle dynamically — UI surfaces snapshot the value
// at View() time but the kitty image upload pipeline holds session
// state that isn't designed for mid-session protocol changes.
var (
	imageModeMu     sync.RWMutex
	imageModeActive bool
	imageModeCells  = 2
	imageScope      = ScopeAll
)

// SetImageMode records whether emoji should be rendered as images.
// cells must be 1 or 2; other values clamp to 2. Safe to call at
// most once during process startup, before bubbletea begins.
func SetImageMode(active bool, cells int) {
	if cells != 1 && cells != 2 {
		cells = 2
	}
	imageModeMu.Lock()
	defer imageModeMu.Unlock()
	imageModeActive = active
	imageModeCells = cells
}

// ImageModeActive reports whether emoji-as-images is enabled.
// Cheap RLock under the hood; safe to call from any goroutine.
func ImageModeActive() bool {
	imageModeMu.RLock()
	defer imageModeMu.RUnlock()
	return imageModeActive
}

// ImageModeCells returns the per-emoji cell footprint (typically 2).
// Returns 2 when image mode is inactive; callers should gate on
// ImageModeActive before using this value.
func ImageModeCells() int {
	imageModeMu.RLock()
	defer imageModeMu.RUnlock()
	return imageModeCells
}

// Scope selects which emoji the image path renders, when image mode is
// active. ScopeAll (the zero value, every existing caller and test)
// images everything with a resolvable URL. ScopeCustom images only
// workspace custom emoji: on a sixel terminal a standard emoji costs
// kilobytes of sixel per repaint where the terminal's own font draws a
// sharp glyph for free, so the balanced default there images only what
// has no glyph at all. Set once at startup, next to SetImageMode.
type Scope int

const (
	ScopeAll Scope = iota
	ScopeCustom
)

// String returns a human-readable scope name (used in logs).
func (s Scope) String() string {
	switch s {
	case ScopeCustom:
		return "custom"
	default:
		return "all"
	}
}

// SetImageScope records which emoji the image path renders.
func SetImageScope(s Scope) {
	imageModeMu.Lock()
	defer imageModeMu.Unlock()
	imageScope = s
}

// ImageScope reports the active scope.
func ImageScope() Scope {
	imageModeMu.RLock()
	defer imageModeMu.RUnlock()
	return imageScope
}

// resetImageMode clears the mode. Test-only helper.
func resetImageMode() {
	imageModeMu.Lock()
	defer imageModeMu.Unlock()
	imageModeActive = false
	imageModeCells = 2
	imageScope = ScopeAll
}
