# Forge Solo's website: how it looks ("Forge look, Home feel")

https://forgesolo.bch2.org is Forge Solo's own home, for people who have never mined. The **look** comes from the Forge concept: the
anvil-and-sparks mark, ember for the one thing to do, copper and rust on workshop paper, Zilla Slab
headings, square plates and dashed wires. The **feel** comes from the Home concept: a light page in
daylight, newcomer-first order and words, one friendly flat drawing style, numbered 1/2/3 parts that
match between the picture and the cards, and warnings set as callouts. The page is light from top to
bottom; the only dark band is the footer (plus the black box that holds the stratum address, and the
ink bezels around the app's own screenshots, which are dark because the app is).

Two pages share the look: the home page and the questions page (`faq/index.html`, at /faq/), with
the same header and footer; `404.html` answers every other address.

## Palette

| Token | Hex | Role |
|---|---|---|
| `--paper` | `#f6efe4` | page ground |
| `--paper-deep` | `#ece2d3` | How it works band (24 px graph-paper grid at 4.5% ink), picture panels |
| `--paper-hi` | `#fcf8f2` | cards and plates |
| `--rule` | `#d3c2ad` | card edges (decorative) |
| `--sand` | `#e2d3be` | drawings: floor, pegboard, screen bars |
| `--ink` | `#1f1813` | text, headings, the anvil, button edges, focus ring |
| `--ink-2` | `#5a493c` | secondary text, the Umbrel-style box in the drawings |
| `--iron` | `#3a2b21` | drawings: the Bitaxe's board |
| `--rust` | `#a3421a` | links, tags, kickers, the dashed wires and arrows on paper |
| `--rust-deep` | `#7f3112` | link hover |
| `--copper` | `#d98c5f` | drawings: traces, fan blades, the hammer (never text) |
| `--ember` | `#ff7a2e` | **the primary action** (buttons), sparks, drawing accents |
| `--ember-deep` | `#e2611c` | the mark's sparks on paper, bullets, the Then plate's top edge |
| `--amber` | `#ffb547` | highlights: "at home.", the drawn coin, the lamp, the address placeholder on black |
| `--amber-tint` | `#ffe6bf` | warning callouts, the "When you're paid" row |
| `--soot` / `--ash` / `--ash-2` | `#15110e` / `#f6eee4` / `#cfc1b0` | the footer band and the stratum-address box |

Contrast (WCAG 2 relative luminance; every rendered text node of the home page, the questions page
and 404.html was also measured against its real background at 1440, 1024, 390 and 320 px: 191 on
the home page and 242 on the questions page at 1440, lowest 4.89:1, all pass, and again with a
glossary word and a question opened from a link):

| Foreground | Background | Ratio | Use | Needs |
|---|---|---|---|---|
| ink | paper | 15.34:1 | body text, headings | 4.5, pass |
| ink | paper-hi | 16.56:1 | text in cards and plates | 4.5, pass |
| ink | paper-deep | 13.67:1 | text on How it works | 4.5, pass |
| ink-2 | paper / paper-hi / paper-deep | 7.50 / 8.09 / 6.68:1 | secondary text | 4.5, pass |
| rust | paper / paper-hi | 5.49 / 5.92:1 | links, tags, kickers | 4.5, pass |
| rust | paper-deep | 4.89:1 | tag and link on How it works (the floor; only at 14 px bold or 18 px) | 4.5, pass |
| rust | amber-tint | 5.17:1 | links inside warning callouts | 4.5, pass |
| rust-deep | paper | 7.82:1 | link hover | 4.5, pass |
| ink | ember | 6.74:1 | primary button label | 4.5, pass |
| ink | amber | 9.97:1 | button hover, the "at home." highlight | 4.5, pass |
| ink | amber-tint | 14.46:1 | callouts, "When you're paid" | 4.5, pass |
| ink-2 | amber-tint | 7.06:1 | a glossary word opened from a link | 4.5, pass |
| paper-hi | ink | 16.56:1 | the 1/2/3 numbers and the "?" | 4.5, pass |
| ink | inline code ground (worst case) | 11.97:1 | `3333`, `rig1` | 4.5, pass |
| ash | soot | 16.33:1 | the stratum address, footer brand | 4.5, pass |
| amber | soot | 10.69:1 | `<your machine's address>`, footer focus ring | 4.5, pass |
| ash-2 | soot | 10.65:1 | footer text | 4.5, pass |
| ash | ink | 15.25:1 | "Open full size" on a screenshot's bezel | 4.5, pass |
| ink | paper-hi | 16.56:1 | the glossary's word index | 4.5, pass |
| rust | paper | 5.49:1 | dashed wires and arrows, the dotted underline of a word to look up (non-text) | 3.0, pass |
| ember-deep | paper | 3.08:1 | the bar beside a question or a glossary word opened from a link (non-text) | 3.0, pass |
| ink-2 | paper-hi | 8.09:1 | chip border, the TIDES window's dashed edge (non-text) | 3.0, pass |
| ink | paper | 15.34:1 | focus ring (non-text) | 3.0, pass |
| ember-deep | paper | 3.08:1 | the mark's sparks (logo) | exempt |
| ember | paper | 2.28:1 | ember fills inside drawings | decorative, always beside words |
| the BCH2 coin's green edge | paper / paper-deep / soot | 2.24 / 1.99 / 7.35:1 | the coin, beside its words or in a described drawing (logo) | exempt |
| black B | the coin's green | 8.22 to 6.75:1 | the coin's B (logo) | exempt |

Ember is never used for text on paper, and the coin's greens never for anything but the coin. Links
are always underlined, never colour alone.

## Type

| Role | Face | Licence | File | Size |
|---|---|---|---|---|
| Display: h1 to h3, the wordmark, the 1/2/3 numbers | **Zilla Slab Bold** (Typotheque for Mozilla, 2017), `font-variant-numeric: lining-nums` (its default figures are oldstyle) | SIL OFL 1.1, `fonts/OFL-ZillaSlab.txt` | `fonts/zilla-slab-700.woff2` | 26,100 B |
| Text, buttons, labels | **Source Sans 3**, variable 200 to 900 (Adobe, Paul D. Hunt) | SIL OFL 1.1, `fonts/OFL-SourceSans3.txt` (Reserved Font Name "Source") | `fonts/source-sans-3-wght.woff2` | 28,872 B |
| What you type: addresses, ports, file names | **Source Code Pro** 500 (Adobe) | SIL OFL 1.1, `fonts/OFL-SourceCodePro.txt` (Reserved Font Name "Source") | `fonts/source-code-pro-500.woff2` | 11,604 B |

All three are the unmodified Latin subsets (U+0000 to 00FF plus general punctuation) from the
Fontsource npm packages, WOFF2, served from the site; 66,576 B in total. Each licence file starts
with the copyright notice the font itself carries, then names the face and weight the site serves
("Source Sans 3 (variable, weights 200 to 900)", "Source Code Pro Medium (500)", "Zilla Slab Bold
(700)"). The subsets have no U+2192 arrow, so menu paths use "›". The only raster images are
the link preview and the home-screen icon (PNG, below) and the app's screenshots (WebP, under
Components).

**The zero check.** "0.00% 1000" was rendered in Source Sans 3, Public Sans, Figtree, Source Code Pro
and Zilla Slab before choosing. Source Sans 3 has a **plain oval zero** by default at every
weight (no slash, no dot), narrower than the capital O, and its l has a tail, so l, I and 1 differ.
Source Code Pro has a dotted zero, which is right for addresses and ports. Inter is not used anywhere.

Scale (rem, so a larger browser font enlarges everything):
- h1 `clamp(2.875rem, 1.5rem + 5.2vw, 5.5rem)` (46 to 88 px), line height 1, tracking -0.02em
- h2 `clamp(2.125rem, 1.45rem + 2.6vw, 3.5rem)` (34 to 56 px), 1.04, -0.015em
- card titles 23 to 26 px; Solo / TIDES 32 to 44 px; platform names 28 to 30 px; Then steps 24 px
- body 17 px, 18 px from 900 px wide, line height 1.6; ledes 18 to 22 px
- tags and kickers 14 px bold, uppercase, tracked 0.1em; flow captions 13 px bold uppercase (ink-2,
  7.5:1), the smallest text on the page; chips 14 px bold
- mono: file names and the app store 15 px; the stratum address 13 px (320 px) to 18 px

## The mark

The Forge anvil, side on, with three tapered sparks: the moment of the strike. On paper the anvil is
ink and the sparks ember-deep (`img/mark.svg`, which `404.html` shows); on dark the anvil is paper and
the sparks ember (`.mark-on-dark` in `css/site.css`: the footer and the box in the drawings). The page draws it once
(`<symbol id="mark">`, which the questions page copies as it is for its own header and footer) and reuses it in the header, the footer, the Umbrel-style box in every drawing
and the dashboard on every screen, where it replaces Home's house.

Refinement for the blend: the favicon (`img/favicon.svg`, soot square, 12/64 corners) uses heavier
sparks (the same angles and lengths, capsules 1.3 to 1.5 times thicker), so at
16 px the three sparks stay three strokes instead of dots. Minimum size 16 px; clear space half the
anvil's height; never in a circle (that is BCH2's coin badge); the mark itself never moves.

**Home-screen icon and link preview.** `img/apple-touch-icon.png` (180 x 180, 1.6 KB) is the favicon
drawn full-bleed on soot with the mark at 75%, centred, because iOS rounds the corners itself.
`img/og.png` (1200 x 630, 29 KB, 256-colour PNG) is the hero in the paper and ember look: the mark and
wordmark, the tag with the BCH2 coin before it (26 px), "Mine BCH2 at home." with the amber highlight,
the lede's words, the hero desk with the BCH2 coin arriving at the phone, and a soot strip with the
three facts and forgesolo.bch2.org. Both were made from the page itself (the drawing parts and the
hero straight out of `index.html`, and the site's own stylesheet, fonts and images), so they match
it: a change to the hero needs them made again. The head carries the `canonical`, `og:*` and
`twitter:*` tags (summary_large_image, @BitcoincashII) and `apple-touch-icon`; `og:image` ends in
`?v=2`, a new number with each new picture, since X, Facebook and Telegram keep a picture by its
address.

## The BCH2 coin

`img/bch2-coin.svg` is Bitcoin Cash II's own coin: `bch2_logo.svg` from
[BitcoincashII/BCH2-Graphics](https://github.com/BitcoincashII/BCH2-Graphics) at commit d8f44f8 ("Add
bch2_logo.svg (vector version of bch2_logo.png: gradient circle, traced II and B)"), byte for byte:
4,682 B, a `#09b887` to `#09a67a` gradient circle, a `#067355` traced "II" and a black B, with no
script and nothing loaded from elsewhere. That repository has no licence file; the coin is the BCH2
project's own mark, so it is used as it is: never recoloured, redrawn, cropped or outlined. Its greens
are the coin's own colour, not an accent of the site: nothing else on the pages is green, and the
anvil and sparks stay Forge Solo's logo (which is why the mark is never put in a circle).

It stands only where it means BCH2, and small:

| Where | Size | Description |
|---|---|---|
| before the hero's tag, "Home mining for Bitcoin Cash II", in place of the tag's rule | 20 px | `alt=""`: the words beside it say it |
| the hero drawing: the reward arriving at the phone, under its sparks | 30 of the drawing's 640 units (28 px at 1440, 17 px at 390, 14 px at 320); pin 3 sits below it, on the phone's edge, and never covers it | the drawing's description says the reward arrives as a BCH2 coin (`fs_hero_art_coin`) |
| What you need, "Your BCH2 address": the coin falling to the phone | 26 of the panel's 240 units (28 px at 1440, 19 px at 390) | none: the drawing is decorative beside its heading |
| the questions page, before "What is Bitcoin Cash II (BCH2)?" | 1 em of the heading (23 to 30 px), centred on its first line; a second line starts under the words | `alt=""` |
| the footer, before "Bitcoin Cash II home miner" | 20 px, centred under the anvil, the words under "Forge Solo" at every width | `alt=""` |
| the link preview, `img/og.png` | 26 px before the tag, and in the desk drawing | `og:image:alt` says a BCH2 coin arrives |

The drawn amber coin (`#coin`) stays where a coin means a payment in general: on the phone's own
screen (the wallet) and at Solo's "Who gets paid", where a block can pay 1175 too. The pages show the
file itself, with `<img>` and, in the drawings, SVG `<image>`: one file, kept by the browser, which the
policy's `img-src 'self'` allows, and drawn sharp at any size. At 20 px it reads as a green coin with a
black B. Its centre is within a pixel of the middle of the capitals beside it, in the tag, the
question and the footer (measured at 1440, 1024, 390 and 320 px), and when a translation wraps, it
stays beside the first line and the second starts under the words.

## Drawings: one flat style

Every picture (the hero desk, the three What-you-need panels, the Solo/TIDES flows, the platform
icons, the Then diagram) is built from the same inline-SVG parts: `#bitaxe` (an upright board with its
display, the fan on the heatsink, copper traces and mounting holes), `#asic`, `#box` (the computer that
stays on, with the anvil), `#monitor` (the Forge Solo dashboard: mark, a level, jittery hashrate line,
no numbers; nothing on it rises like earnings), `#phone` (the wallet), `#coin`, `#block` (a block seen
straight on, like everything else: an amber header band over three lines), `#fan` and the mark. Every
object is drawn straight on; nothing is isometric. Flat fills from the tokens only, no outlines, no
gradients, rounded corners, a sand floor line. The one exception is the BCH2 coin, the reward arriving
at the phone: BCH2's own artwork, as it is (above). The desk's drawer hangs from the top and meets the
right leg. "Others" in the TIDES window are the same Bitaxe in paper tones. The hero numbers 1, 2,
3 are HTML pins over the picture (so they stay legible on a phone; 3 sits a fifth of the way down the
phone's edge, clear of the BCH2 coin above it) and a key under it ("A miner, like a Bitaxe", "A computer that stays on", "Your BCH2
address") matches the three cards, whose numbers come from the list itself (CSS counters).

The TIDES picture shows no proportions: "Who gets paid" is the fraction *your work / all work in the
window*, in words, so nothing reads as a typical share. On phones the line under it ("Everyone in the
window, by their work") is set apart in ink-2, so the three lines do not read as one fraction.

Motion: the fans turn, the box's light blinks, the coins bob; all of it only under
`prefers-reduced-motion: no-preference` (checked: zero animations when reduced). Scrolling is never
smooth: smooth scrolling also animates the jump when a page opens on a part of itself, so a glossary
word linked from the home page would scroll past the whole questions page first.

## Components

- **Plates**: paper-hi, 1 px rule edge, 8 px corners, a 4 px ink top edge for the Solo/TIDES and
  platform cards, ember-deep for the Then plate (the last thing to do). What-you-need cards have no
  top edge and a picture panel instead.
- **Wires**: dashed rust lines with an arrowhead, labelled "Connects on port 3333" and "Pays rewards
  to". Horizontal between the cards on wide screens; vertical with the label beside them on phones,
  so the port never disappears. The label is real text in the card's flow, so screen readers read it
  between the two cards. Under forced colors (Windows contrast themes) the dashes are backgrounds the
  theme would remove, so the wires and the flow arrows become solid CanvasText lines (checked).
- **Buttons**: ember with a 2 px ink edge and a 4 px ink shadow (Home's pressable sticker, Forge's
  6 px corners); the secondary is an ink outline. At least 44 px tall.
- **Callouts**: amber-tint with a warning triangle for things that will go wrong: the two Windows
  prompts (one full-width callout under the three platform cards, the prompts numbered side by side
  from 760 px, and the Windows card points to it) and the Private network. Under 560 px the triangle
  sits beside the title only, so the words below use the full width. Side notes (rentals) are a plain
  line, not a callout.
- **Reference panel**: paper, 1 px rule, a key icon: "Where to find the dashboard and the password",
  one column per platform from 760 px, one paragraph per platform below that. It sits under both
  setup steps, since both need the dashboard.
- **Code boxes**: the app store URL and file names on paper; the stratum address on black. Each is
  built from no-wrap segments with a `<wbr>` only at sensible points (after `https://github.com/`,
  after `BitcoincashII/`, after `stratum+tcp://`, after `forge-solo-<version>-`, so no line starts
  with a hyphen that looks like a command flag), and scrolls inside its own box if it ever has to.
  Checked at 390 and 320 px: no segment ever breaks. Every literal (URL, file names, ports, `uname -m`,
  `rig1`, `DASHBOARD_PASSWORD`, the arch names, the fixed parts of the stratum address) carries
  `translate="no"`; the placeholder words inside the stratum address stay translatable.
- **Focus**: a 3 px ink ring, 3 px out, everywhere on paper (around a screenshot's bezel too); amber
  in the footer. Skip link first.
- **Words to look up**: the first use on the home page of a word a newcomer may not know (BCH2 node,
  ASIC miner, hashrate, merge-mine, stratum address, worker name) links to its entry in the
  glossary (`/faq/#glossary-<word>`). Such a link keeps the text's colour and has a dotted rust
  underline (`a.term`), solid on hover, so it reads as "look this up" and not as a way out of the
  page. The answers on the questions page do the same, once per answer. Only the hard words: a word
  the sentence already explains in plain words stays plain.
- **Questions after a section**: a line with a round ink "?" (the 1/2/3 numbers' style) and a link to
  the answer on the questions page: under What you need ("Questions before you start?"), under How
  it works ("Still deciding?", to Solo or TIDES) and after the screenshots ("Not mining yet?").
- **Screenshots** (What you'll see, after Set up, on paper-deep): real screenshots of Forge Solo
  1.0.16 on a test install (Settings under How blocks pay before an address is saved, the dashboard
  in TIDES mode and in Solo mode, each down to its Wallet card), each in an 8 px ink bezel with
  10 px corners like the drawings' monitor, and a caption under it: a Zilla Slab title and a few
  plain sentences of what to look at. The lede says the figures in them come from a test miner and
  are not yours, and no text quotes them, except to say why some are 0: the test miner is too small
  to reach Forge Pool's share difficulty (the TIDES card) and has found no block, and the test
  address holds no coins (the Wallet card). On the page a screenshot is smaller than the app, so its
  bezel is a link to the `@2x` file, where the app's words are at their own size; the bezel's chin
  says "Open full size" (ash with an ember underline, as the footer's links, and an amber icon).
  Settings and the TIDES dashboard sit side by side from 900 px, their titles lined up under the
  taller TIDES picture; the Solo dashboard takes the next row, as wide as each picture above it, so
  all three show the app at one size, with its words beside it. Below 900 px they stand one under
  another, 36 rem at most.
  WebP, `width`/`height` from the file, `srcset` at 1x and 2x widths with `sizes`, lazy-loaded.
  The crops leave out anything that showed the test machine's address, and other miners' payout
  addresses in the TIDES blocks table are blurred.
- **Ready to start?**: the last thing in the home page's and the questions page's main part, a plate
  with the Then plate's ember edge, "Ready to start?" and the primary button to Get it, since the
  header is not fixed and the footer has no download.
- **Not found**: `404.html`, the answer to any address the site has no page for: the masthead's mark
  and name, "Page not found", one sentence and the primary button home. It links its files from the
  site's root, since nginx serves it at whatever address was asked for.

## The questions page

`faq/index.html`, "Questions and words": twelve questions a newcomer asks before mining, and a
glossary of the words they will meet, for Forge Solo 1.0.16. It is for reading in any order, so it
is plain: a head on the How it works graph paper (tag, h1, lede), then the list of questions and the
answers. The list ("On this page") is a plate like the platform cards: from 1000 px it is a column
beside the answers, and stays in view while they scroll when the window is at least 760 px tall;
below that it sits above them, in two columns from 700 px. Each answer is a Zilla Slab question,
short paragraphs (lists for choices, numbered for steps, with the page's diamond and round numbers)
and a "Details:" line with links to the README section, the release page or Forge Pool's own pages
where the rest is. Answers are anchored (`#solo-or-tides`, `#not-mining-yet`, ...), and the one a
link opens on is marked with an ember-deep bar. The glossary starts with every word as a small
link plate in a few wrapping lines (on a phone the glossary is long), then the words: a two-column
set of small plates (one column under about 610 px), each anchored as `#glossary-<word>`, tinted
amber with the same ember-deep bar when a link opens on it (a border, so a Windows contrast theme
keeps it). The page ends with the "Ready to start?" plate.

## Layout

Content 1200 px, side gutter `clamp(16px, 4vw, 48px)`, sections `clamp(56px, 7vw, 104px)` (52 px on
phones). Header (the same on the home page and the questions page): brand, nav (What you need, How
it works, Set up, Questions, GitHub), "Get Forge Solo" on one row; under 960 px the nav takes its
own row; under 460 px GitHub leaves the nav (it is on every platform card and in the footer), under
390 px Set up does too (the header's button leads to Get it, which Set up follows), and under 375 px
the button reads "Get it", so the nav never wraps (checked from 320 to 1440 px). The questions
page's own link is underlined in ink there (`aria-current="page"`). Under forced colors the theme
would draw every link's transparent underline, so it is drawn in Canvas, and only the current page's
link (and the one under the pointer) is underlined.
Subgrid aligns the three platform cards' rows (so the buttons line up), the two mode cards' rows and
the three flow stations. A subgrid must not be a size container (containment makes it an independent
grid), so the platform card's head, not the card, is the container that moves the chip under the
name in a narrow card. The flows keep their rows at their own height (`align-content: start`) and the
shorter "When you're paid" box keeps its own height, so no box is padded with empty colour.

## Tone of voice

Plain words, second person, one idea per sentence; the term after the plain words, once ("your own
BCH2 node, which keeps a full copy of the blockchain"; "its stratum address, the server it sends its
work to"). Mining in two sentences before any mode. Name what the visitor will see in the app
("Settings, under How blocks pay"). Say what will go wrong before it does (the Windows prompts, the
Private network, the password prompt), and what to choose when it does. No unexplained jargon:
"coinbase" is not used on the home page (the block pays "straight from the block itself"; the
glossary explains it, for the dashboard's sake); Forge Pool is introduced with
its address, the TIDES window as where Forge Pool registers the work of everyone mining into it
through Forge Solo in TIDES mode or Forge Gateway (not the miners on Forge Pool's own ports), and a
TIDES block as "a block found on work Forge Pool registered". Only facts from the READMEs at v1.0.16
(root, `packaging/linux`, `windows`), its release notes or the page this one replaced: no odds, no earnings, no chain size, no "easy",
no exclamation marks.

## Translations

Every visible text node sits under a `data-i18n` key (`fs_*`; `site_test.go` checks it), including
the picture's description and the 1/2/3 key. Numbers in the pins come from CSS, so they need no key.
A key is shared with the page this one replaced (31def56) only where the English is identical (15
keys: the nav's How it works and Set up, the platform names, Get Forge Solo, the footer and a few
more; on `404.html`, "Page not found"); every key whose words changed has a new name
(`fs_hero_headline`, `fs_hero_summary`, `fs_how_nosched`, `fs_tides_name`, `fs_get_umbrel_add`,
`fs_get_windows_text`, `fs_get_linux_text`, `fs_get_win_*`, `fs_404_line` and so on), so a
translation made for the earlier page never shows the wrong words. The questions page's own texts
are `fs_faq_*` (`fs_faq_<question>_q` for a question, wherever it appears) and its glossary's
`fs_word_<word>` and `fs_word_<word>_def`; the header and footer keep the home page's keys. The
screenshots' descriptions are keyed in `data-i18n-alt` (`fs_shot_*_alt`), their captions in
`data-i18n` like any text, and their "Open full size" is `fs_shot_open`. The Ready plate's line is
`fs_ready` on both pages, and the glossary's word index reuses the words' own `fs_word_<word>` keys.

## How it is not Forge Pool

Forge Pool is a near-black dashboard with emerald, Inter and rounded dark cards, built for watching
numbers. Forge Solo is a lit workshop: warm paper, ink and ember, a slab display face with a humanist
sans, square plates, dashed wires and drawings of a desk with a miner on it.

## Files

In `site/`: `index.html`, `faq/index.html`, `404.html`, `css/site.css`, `fonts/` (three WOFF2 files
and their OFL licences), `img/mark.svg`, `img/favicon.svg`, `img/apple-touch-icon.png`, `img/og.png`,
`img/bch2-coin.svg` (BCH2's coin, from BCH2-Graphics at d8f44f8), and the screenshots `img/forge-solo-settings-how-blocks-pay.webp`,
`img/forge-solo-dashboard-tides.webp` and `img/forge-solo-dashboard-solo.webp`, each with its `@2x`
file. No script;
everything works under the site's CSP (`default-src 'none'; style-src 'self'; font-src 'self';
img-src 'self'`). [README.md](README.md) says how the site is served and deployed.
