# Title normalization

## Purpose and ownership

This document is the source of truth for cleaning Torznab search queries,
extracting season and episode metadata from AniLiberty torrent labels, matching
`tvsearch` filters, and rendering deterministic Sonarr-friendly titles. Torznab
parameter and XML rules belong in [torznab-contract.md](torznab-contract.md),
upstream JSON fields in [integrations/aniliberty.md](integrations/aniliberty.md),
and orchestration in [architecture.md](architecture.md).

**Status:** first-release parsing baseline

**Last updated:** 2026-07-16

## Inputs and outputs

The parser receives:

- AniLiberty `release.type.value`;
- `release.name.main`;
- torrent `label`; and
- optional Torznab `season` and `ep` filters.

It returns one shared representation:

```text
Season: integer or absent
Episodes: absent, one integer, or inclusive start/end integers
Title: rendered string
```

Filtering and title rendering MUST consume the same parsed values.

## Output grammar

For a serial result with episodes:

```text
<main name> / <original label> S<season>E<start>[-E<end>] RUS
```

For a serial result without parsed episodes:

```text
<main name> / <original label> S<season> RUS
```

For a movie without parsed episode data:

```text
<main name> / <original label> RUS
```

Season and episode numbers are padded to at least two digits, not truncated:
`1` becomes `01`, `12` stays `12`, and `123` stays `123`.

The original label is preserved byte-for-byte except for trimming leading and
trailing Unicode whitespace. Its quality, codec, distributor tag, punctuation,
and bracket groups are not rewritten. Exactly one ASCII space separates the
constructed segments.

`release.name.main` and a non-blank label are required upstream data. The first
release does not invent a title from `name.english` when `name.main` is missing.

## Release classes

Serial behavior applies to:

```text
TV, ONA, WEB, OVA, OAD, SPECIAL, DORAMA
```

`MOVIE` is special: when no episode token is present, the renderer adds neither
a synthetic season nor an episode. If a movie label explicitly contains a valid
episode token, the parser MUST preserve that explicit metadata, use default
season 1, and render it. It never fabricates an episode.

## Parsing regions

Season detection uses the semantic title portion: label text before the first
technical `[` group. From that region it removes, case-insensitively, only a
trailing distributor suffix of the exact form
`- (AniLiberty|AniLibria)(\.(TOP|TV))?`, with surrounding whitespace allowed.
Episode detection uses bracket groups because quality labels place episode
ranges there.

Parsing is Unicode-aware for whitespace but recognizes ASCII keywords and
digits case-insensitively. Hyphen-minus, en dash, and em dash are accepted as
episode range separators.

## Season detection

Apply the following precedence and stop at the first valid match:

1. `Season N`, `S N`, `SNN`, or `Series N` as complete tokens;
2. English ordinal season forms `Nst Season`, `Nnd Season`, `Nrd Season`, or
   `Nth Season`, only when the suffix is correct for the English ordinal number
   including the `11th`-`13th` exception;
3. a valid standalone Roman numeral at the end of the semantic title after an
   optional four-digit year has been removed, such as `Title II`;
4. default season 1 for serial releases.

Accepted Roman numerals are canonical values `I` through `XX` in the first
release. A loose sequence of Roman-numeral letters is not enough; parse and
round-trip it to its canonical uppercase form.

Safeguards:

- `Part N` is never a season signal.
- If both `Season 2` and `Part 3` occur, explicit `Season 2` wins.
- A four-digit year in plain text or parentheses is ignored.
- A bare Arabic number is never a season signal. This prevents titles such as
  `86` from becoming season 86.
- Numbers inside technical bracket groups do not participate in season parsing.
- Resolution, codec, bit depth, and audio-channel numbers are never seasons.

If a recognized token contains zero or exceeds the configured parser safety
limit of 999, it is invalid rather than defaulted. The item remains renderable
with no parsed explicit season; serial rendering then uses the documented
default season 1.

## Episode detection

Inspect bracket groups from right to left and select the first group whose
trimmed content is a complete accepted episode expression:

```text
N
N-M
E N
E N-M
EP N
EP N-M
Episode N
Episodes N-M
```

Spaces around tokens and the range separator are allowed. Matching is
case-insensitive. Leading zeroes are accepted and removed in the numeric model.

Examples of accepted groups:

```text
[1]
[01-12]
[E03]
[Episodes 7–9]
```

Examples that are not episode groups:

```text
[1080p]
[x264]
[AVC]
[10-bit]
[5.1]
[1-12 + OVA]
```

The parser deliberately rejects mixed expressions such as `[1-12 + OVA]` in
v1 instead of partially guessing. The result is retained under the no-episode
rendering rule.

Episode numbers must be positive and no greater than 9999. For a range, end must
be greater than or equal to start. Equal bounds normalize to a single episode.

## Parser failure behavior

Failure to find a season or episode pattern is not an item failure:

- serial releases use default season 1 and may render without `E...`;
- movies render without synthetic season/episode metadata; and
- the original label remains present in the title.

Invalid required upstream data, such as an empty label or missing main name, is
not a parsing failure and is handled by upstream validation.

## Query cleanup

Query normalization returns a single value containing `CleanQuery`, optional
`EffectiveSeason`, optional `EffectiveEpisode`, and `HadTechnicalTokens`.
Explicit `season`/`ep` parameters are normalized first; then Prowlarr or Sonarr
tokens embedded in `q` fill any missing effective value. Before AniLiberty
release search, remove only complete, unambiguous tokens:

```text
S02E03
S02 E03
S02
E03
2x03
Season 2
Episode 3
Ep 3
```

Every listed complete token is removed and supplies its season and/or episode
when the corresponding explicit parameter is absent. This includes standalone
`S02`, `Season 2`, `E03`, `Episode 3`, and `Ep 3`. When an explicit parameter is
present, the token must agree with it.

Conflicting values are an incorrect parameter error; they are not silently
rewritten. For example, `q=Title S02E03&season=1&ep=3` returns error `201`.

Cleanup MUST NOT remove:

- years;
- bare numbers;
- `Part N`;
- Roman numerals;
- numbers embedded in words; or
- punctuation other than now-empty token separators.

After token removal, collapse Unicode whitespace to single ASCII spaces and
trim separators that became isolated at either edge. Do not transliterate,
lowercase, or remove punctuation from the title sent upstream.

For `search`, an empty cleaned query selects the latest flow only when the
original `q` was missing or blank. A non-blank query made empty solely by token
cleanup is an incorrect parameter rather than an implicit latest request. For
`tvsearch`, an empty cleaned query is always an incorrect request.

Normative query examples:

| Input | Explicit parameters | Normalized result |
| --- | --- | --- |
| `Title S02E03` | none | query `Title`, season 2, episode 3 |
| `Title Season 2` | `ep=E03` | query `Title`, season 2, episode 3 |
| `Title S02` | `season=S02` | query `Title`, season 2, no episode |
| `Title S02E03` | `season=1`, `ep=3` | error `201` |
| `Title Part 2` | none | query unchanged, no effective filter |
| `S02E03` | none | error `201` because cleaned query is empty |

## Filter matching

- An effective `season` matches the parsed explicit season or the serial default
  season 1.
- An effective `ep` matches a single episode exactly.
- An effective `ep` matches a range when `start <= ep <= end`.
- An item with no parsed episodes does not match an episode filter.
- A movie with no parsed season does not match a season filter.
- Category filtering is independent and is applied in addition to these rules.

## Normative examples

`<RU>` below represents the non-empty `release.name.main`; the complete label is
shown to make preservation visible.

| Type | Label | Parsed | Rendered suffix/result |
| --- | --- | --- | --- |
| `TV` | `Let's Go Kaiki-gumi - AniLiberty.TOP [WEB-DL 1080p][AVC][1-2]` | S1, E1-E2 | `<RU> / <label> S01E01-E02 RUS` |
| `TV` | `Example Season 2 - AniLiberty.TOP [WEB-DL 1080p][13]` | S2, E13 | `<RU> / <label> S02E13 RUS` |
| `TV` | `Example 2nd Season - AniLiberty.TOP [WEB-DL 1080p][1-12]` | S2, E1-E12 | `<RU> / <label> S02E01-E12 RUS` |
| `TV` | `Example II - AniLiberty.TOP [WEB-DL 1080p][03]` | S2, E3 | `<RU> / <label> S02E03 RUS` |
| `TV` | `Example Part 2 - AniLiberty.TOP [WEB-DL 1080p][04]` | S1, E4 | `<RU> / <label> S01E04 RUS` |
| `TV` | `86 - Eighty Six - AniLiberty.TOP [WEB-DL 1080p][01]` | S1, E1 | `<RU> / <label> S01E01 RUS` |
| `TV` | `Example (2024) - AniLiberty.TOP [WEB-DL 1080p][1]` | S1, E1 | `<RU> / <label> S01E01 RUS` |
| `TV` | `Example - AniLiberty.TOP [WEB-DL 1080p][1-12 + OVA]` | S1, no episodes | `<RU> / <label> S01 RUS` |
| `MOVIE` | `Example Movie - AniLiberty.TOP [WEB-DL 1080p][AVC]` | no S/E | `<RU> / <label> RUS` |
| `MOVIE` | `Example Movie - AniLiberty.TOP [WEB-DL 1080p][1]` | S1, E1 | `<RU> / <label> S01E01 RUS` |

These cases MUST be table-driven unit tests. Add regression rows for every
parsing bug; do not broaden a regular expression without a counterexample test
for likely false positives.

## Implementation guidance

Prefer small staged parsers over one compound regular expression:

1. identify semantic and bracket regions;
2. parse explicit season forms;
3. parse a constrained Roman suffix;
4. inspect bracket groups for a complete episode expression; and
5. render from numeric values.

Compile regular expressions once. Keep parsing pure and independent of HTTP or
XML packages. Fuzz tests are useful for panic safety and invariant checks, but
the normative table is the primary behavioral contract.
