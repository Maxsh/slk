# Emoji as sixel images: foot, Windows Terminal, xterm

Status: design for implementation. Builds on the WezTerm emulated-kitty
design (2026-10-20) and reuses its resolver and painter unchanged.

## Problem

Emoji render as images only when the final protocol is `ProtoKitty`
(`cmd/slk/main.go`, the two `emojiwidth.SetImageMode` calls: kitty and
Ghostty natively, WezTerm through emulated placement). On a sixel
terminal (foot, Windows Terminal, xterm) emoji image mode is forced
off, so:

- workspace custom emoji (`:pepo-perfect:`) always render as `:name:`
  text — they have no Unicode form, so no glyph fallback can help;
- before the mode-2027 glyph fix (commit de7a7cf5), the whole
  base+VS16 / ZWJ / flag family rendered as `:name:` too; today it
  renders as glyphs, but still not as the workspace's chosen image.

The reason image mode is off is real, not an oversight: `emoji.Place`
hard-codes the kitty protocol. Its warm path looks the fetcher's
prerender memo up under `imgpkg.ProtoKitty`
(`internal/emoji/place.go`) and its flushes fire into
`imgpkg.KittyOutput` (kitty APC). The fetcher prerenders under the
terminal's actual protocol (`Fetcher.ConfigurePrerender(proto)`), so on
foot the warm path would never hit and every render would re-spawn a
fetch — the fetch→ready-msg→re-render loop the reconcile comment calls
out (see issue #50).

## What exists and is reused unchanged

The WezTerm work built the machinery that makes inline images in
flowing text possible at all. None of it is kitty-specific except the
final paint bytes:

- **Placeholder cells.** `emoji.Place` (kitty warm path) returns one
  U+10EEEE rune per cell, truecolor foreground = image ID, diacritics =
  row/column in the image. bubbletea lays them out and wraps them like
  text; the render cache treats them like text.
- **The resolver.** `imgpkg.ResolvePlaceholders` /
  `PlaceholderResolver.Resolve` (`internal/image/placeholders.go`)
  parses the finished frame with ultraviolet, blanks every placeholder
  cell (keeping the SGR, so pill and selection backgrounds survive),
  and reports absolute `PlaceholderRect`s: screen X/Y, visible
  Cols/Rows, and SrcCol/SrcRow for an image cut on the left or top.
  The image ID is decoded from the SGR foreground, whatever
  re-encoding lipgloss and the overlays did.
- **The painter.** `SixelPlacement` (`internal/image/sixelpaint.go`)
  already carries protocol-agnostic fields: `Key`, slot, `Bytes`,
  `Guard`, `Erase`. `FrameOutput` erases before the text diff and
  paints after it, skips identical slots, repaints on guard changes,
  and forces a full plan when the diff moves lines. The
  kitty-emulated placements ride it today with kitty bytes; sixel
  bytes are just different bytes.
- **The ID registry.** `Registry.Lookup(key, target)` mints
  process-unique, never-reused image IDs; `buildPlaceholderLines`
  turns (id, cells) into the placeholder row. Both live in
  `internal/image` and are protocol-neutral.
- **The overlay rule.** `overlay.DimmedOverlay` blanks placeholder
  cells under a modal backdrop, so only surviving placeholders resolve:
  emoji hidden behind a modal vanish, emoji inside it (the reaction
  picker's) stay — the exact semantics the kitty-emulated path ships.

## Decision

Run emoji as placeholders on sixel exactly as the emulated-kitty path
does, and paint the resolved rectangles with sixel payloads instead of
kitty placement commands. `Protocol` stays `ProtoSixel`; no new
Protocol value, per the WezTerm design's warning ("a new Protocol
value is how issue #50 happened").

## Design

### 1. `emoji.Place` becomes protocol-aware

`emoji.PlaceContext` gains the emoji render protocol, set once at
startup next to `emoji.SetImageMode`:

```go
type PlaceContext struct {
    Fetcher  PlaceFetcher
    SendMsg  func(msg any)
    Protocol imgpkg.Protocol // ProtoKitty (default, today's behavior) or ProtoSixel
}
```

- **Warm lookup** queries `ctx.Fetcher.Prerendered(key, target,
  ctx.Protocol)` instead of the hardcoded `ProtoKitty`. On foot the
  fetcher already prerenders under `ProtoSixel` — the messages pane
  calls `ConfigurePrerender(ctx.Protocol)`
  (`internal/ui/messages/model.go`) — so the warm path starts hitting
  and the issue-#50 loop is closed at the root.
- **Warm output** is the same placeholder row for both protocols,
  built by a new exported `imgpkg.PlaceholderLinesFor(id, cells)`
  (wraps `buildPlaceholderLines`), with the ID minted through the
  shared registry. Under kitty, `OnFlush` stays today's APC upload.
  Under sixel, `OnFlush` is **nil**: there is no side-channel upload;
  the payload reaches the terminal through the painter.
- **The sixel payload** comes from the warm prerendered Render the
  lookup just returned: its `OnFlush` writes the sixel byte stream, so
  the first warm hit captures it into a new store (§2) and every later
  hit reads it from there. The Render's `Fallback` half-block rows are
  stored alongside for the partial-visibility case.
- **Cold path** is today's: spaces, nil flush, one deduped fetch,
  `EmojiImageReadyMsg` on completion. The next warm hit swaps the
  spaces for placeholders at identical width.
- **Callers need no changes.** Every surface that fires emoji flushes
  (messages body/reactions, thread, sidebar headers and DM status,
  reaction picker, channel finder, DM topic) loops over its flush
  slice and fires into `KittyOutput`; under sixel the slices stay
  empty, so the loops no-op. `ReactionPillText`'s `placedAsImage`
  foreground re-assertion and `renderEmojiTokensInline`'s trailing
  `\x1b[39m` handling work on placeholder rows unchanged.

### 2. The sixel emoji payload store

New in `internal/image`:

```go
// SixelEmojiStore holds, per image ID, the sixel payload encoded for
// the emoji's cell footprint and the half-block fallback rows.
type SixelEmojiStore struct{ ... }
func (s *SixelEmojiStore) Put(id uint32, cells image.Point, payload []byte, fallback []string)
func (s *SixelEmojiStore) Get(id uint32) (SixelEmojiEntry, bool)
func (s *SixelEmojiStore) Clear()   // cell-pixel change
```

`emoji.PlaceContext` carries a `*SixelEmojiStore` alongside the
protocol. One store per process, like the registry it is keyed into.
Memory: a 2x1-cell sixel payload is ~1-2 KB per distinct emoji; even a
session that touches a thousand emoji stays in low megabytes.

### 3. Enabling image mode on sixel

The post-probe reconcile in `cmd/slk/main.go` becomes:

```go
reconciledImageMode := (proto == imgpkg.ProtoKitty || proto == imgpkg.ProtoSixel) &&
    cfg.Appearance.EmojiImages == "on"
```

The pre-probe block stays kitty-only: it only decides whether the
emoji width probe can be skipped, and its OFF is corrected by the
reconcile a moment later. `[appearance] emoji_images = "off"` keeps
today's text behavior on every protocol.

### 4. The frame exit (`finalizeSixelView`)

Today the sixel branch stamps guards on `v.Content` and publishes the
pane placements; the emulated-kitty branch resolves placeholders and
*replaces* the placements (under kitty no pane produces sixel rows).
With emoji on sixel both sources coexist — pane SixelRows for block
images and avatars, resolver rects for emoji — so:

1. If emoji image mode is on under `ProtoSixel`, run the resolver on
   the finished frame first (own memo, same shape as `kittyFrame`).
   `v.Content` becomes the blanked frame.
2. Stamp the pane placements' guards against the blanked frame (the
   bytes bubbletea will actually draw — placeholder graphemes became
   spaces, so a pre-blank stamp could mismatch).
3. Convert emoji rects to placements and append:
   - **Full rectangle** (the normal case: a 2x1 emoji): `SixelPlacement`
     with `Bytes` = the store's payload for the rect's ID, `Key` =
     `sixel-emoji:<id>@<srcCol>,<srcRow>`, `Erase` unset (ECH is
     correct for sixel), slot from the rect.
   - **Partial rectangle** (SrcCol/SrcRow ≠ 0, or fewer visible cells
     than the image's footprint — an emoji cut by truncation): skip the
     placement. Sixel cannot crop in the placement command and the
     frame-level fallback graft the panes do for block images does not
     apply at frame exit, so the cut emoji's visible cells stay blank.
     Rare (wrap moves whole emoji to the next line; only truncation
     slices them); recorded here as a known limitation, revisit if it
     shows.
4. `RepaintOnScroll` stays false for sixel, as today: emoji placements
   ride the same plan and the same scroll behavior as the existing
   attachment placements, whatever that behavior is on each terminal.
5. The overlay rule is inherited, not ported: resolver-based emoji
   placements are *not* gated on `overlayActive()`, exactly like the
   kitty-emulated ones — the backdrop-blanked placeholders already
   vanished, and a picker's emoji sitting on the modal's own rows is
   correct to paint. The pane-placement gate is untouched.

### 5. Cell-pixel changes

`applyCellPixels` (`cmd/slk/cellpixels.go`) already invalidates the
fetcher's prerender memo and the avatar cache, then forces a full
repaint. It additionally clears the sixel emoji store (payloads are
encoded against the old pixel size) and invalidates the App's emoji
frame memo. The next warm hit re-encodes lazily.

### 6. Width and mode 2027

Unchanged from the kitty path and therefore already proven: an emoji
occupies `ImageModeCells()` placeholder cells; `emoji.Width` counts
image clusters at that footprint; mode 2027 makes multi-codepoint
clusters measure 2 in both the terminal and lipgloss. Image mode
bypasses `ShouldRenderUnicode` entirely — every standard and custom
emoji with a resolvable URL becomes an image; the glyph fallback only
serves names with no URL (unknown shortcodes).

### 7. Startup ordering

`SetImageMode` and the `PlaceContext.Protocol` are set after the
probes, before bubbletea starts — the same place the current reconcile
runs. Surfaces snapshot the context at View time, so no mid-session
protocol switches are introduced (same constraint as today).

## Risks

- **Bandwidth.** A sixel placement re-sends its payload when painted.
  Steady state is quiet (the painter skips identical slots); a forced
  repaint (resize, external-editor return, cell-size change) re-sends
  every visible emoji: 20 visible emoji ≈ 20-40 KB once, the order of
  one inline attachment. Scrolling emits payloads only for newly
  painted slots.
- **Sixel band quantization.** gosixel encodes at the requested pixel
  size; a 2x1-cell image at an odd cell height rounds to 6-pixel bands
  inside the payload but the raster attributes claim the exact size,
  which foot honors. The existing attachment pipeline ships the same
  property.
- **The issue-#50 loop** returns if any future code path makes the
  warm lookup miss under sixel (e.g. a `ConfigurePrerender` call with
  the wrong protocol). A regression test pins it: after a fetch
  completes, a sixel `Place` must not spawn another fetch.
- **Two ID consumers, one registry.** Kitty uploads and sixel payloads
  share the ID space; IDs minted under one protocol are never painted
  by the other because a session runs one protocol. Assert the store
  and the kitty renderer never hold the same ID in a test.

## Testing

- `internal/image`: store put/get/clear; `PlaceholderLinesFor` shape;
  payload capture from a prerendered sixel Render.
- `internal/emoji`: sixel warm returns placeholder + registered
  payload + nil flush; kitty warm unchanged; cold returns spaces and
  no second fetch after the ready msg (issue-#50 regression); negative
  cache unchanged.
- `internal/ui`: sixel frame with emoji resolves to blanked content +
  placements appended after the pane's; pane guards stamped against
  the blanked frame; modal backdrop blanks emoji, picker emoji
  survive; partial rect skipped; cell-size change clears the store.
- Golden: one sixel-mode frame with emoji in a body, a pill and the
  picker, mirroring `emulatedKittyApp`.
- Manual on foot: message bodies, reaction pills (including two
  identical emoji side by side — two rects, not one), custom emoji,
  the reaction picker over a backdrop, emoji autocomplete, sidebar
  section headers and DM status, the DM topic, thread pane, scroll
  fast through an emoji-heavy channel, open/close modals, resize,
  font zoom, theme switch, `ctrl+e` and back.

## Phases

1. `internal/image`: `PlaceholderLinesFor`, `SixelEmojiStore`, tests.
2. `internal/emoji`: `PlaceContext.Protocol`, the sixel branch, tests.
3. `internal/ui`: the `finalizeSixelView` resolver branch, its memo,
   guard re-stamping, tests.
4. `cmd/slk`: the reconcile change, `applyCellPixels` store clear.
5. Goldens, then the manual pass on foot.

## Implementation notes

Where the implementation differs from the sections above, or was
decided after measuring:

1. **The store clear lives in the `CellMetricsChangedMsg` handler**
   (`internal/ui/reducer_io.go`), not in `applyCellPixels`: the handler
   already invalidates every other pixel-size-keyed cache on the Update
   goroutine, and the App owns its store reference there. `cmd/slk`
   only creates the store and wires it into both the emoji
   `PlaceContext` and `app.SetEmojiSixelStore`.
2. **Phase 5's golden became behavioral tests**
   (`internal/ui/sixel_emoji_test.go`): blanking + placement, two
   adjacent identical emoji, missing payload, partial rect, the
   finalize merge with re-stamped pane guards, the memo hit, and the
   modal case. The kitty-emulated pipeline has no frame goldens either;
   the resolver and painter it shares with this path are pinned by
   their own tests.
3. **`PlaceContext.Protocol`'s zero value stays kitty behavior** —
   every existing caller and test builds a `PlaceContext` without the
   field, and `ProtoOff` (the zero `Protocol`) normalizes to
   `ProtoKitty` inside `Place`. Callers gate on image mode before
   calling, so the explicit `ProtoOff` case is unreachable.
4. **Scope: on sixel, only custom emoji become images.** Measured with
   the real Slack CDN assets (16x16 sources encoded at the emoji cell
   footprint): a 2x1-cell sixel payload is 5.6-8.6 KB depending on the
   terminal's cell pixel size, encoding 0.1-2.4 ms per emoji off the UI
   thread. Memory is fine (about 12 KB per distinct emoji in cache and
   store). Bandwidth is the cost: sixel re-sends the whole payload on
   every repaint of its row, and scrolling rewrites every visible row,
   so 20 visible emoji cost about 120 KB per scroll step. The painter's
   slot skipping keeps steady-state quiet; the scroll case is what
   rules out all-emoji sixel as a default. Since the mode-2027 glyph
   fix draws every standard emoji sharply from the terminal's own font,
   sixel images are reserved for what has no glyph: workspace customs.
   `emoji.SetImageScope` (ScopeAll zero-value = today's behavior,
   ScopeCustom) gates `emoji.Place` on the URL: standard assets (this
   package's `CDNBaseURL`, custom aliases to them included) decline so
   every surface falls through to its glyph rule. The image path's
   token fallback (`renderEmojiTokensInline`) now runs the legacy
   pipeline — skin tone stripped, glyph when `ShouldRenderUnicode`
   passes, else `:name:` — so a declined emoji draws the same thing in
   a body and in a pill. `emoji.Width` delegates to lipgloss entirely
   under ScopeCustom (glyphs and placeholder cells both measure
   correctly without the image-cell override). The reconcile sets
   ScopeAll for kitty, ScopeCustom for sixel; `[appearance]
   emoji_images` stays the on/off master switch.
5. **Sixel transparency.** go-sixel hardcodes the DECSIXEL introducer
   `P0;0;8q` (P2=0): the terminal paints every pixel the data leaves
   unset with its own background, a black box behind any transparent
   image on foot. `SixelRenderer.Render` patches its own output to
   `P0;1;8q` (transparent background) and thresholds the scaled image's
   alpha at the halfway point, because bilinear scaling blends a
   transparent edge into the empty canvas it lands on and quantizes to
   dark half-drawn border pixels. Both changes serve attachments,
   avatars and previews too.
