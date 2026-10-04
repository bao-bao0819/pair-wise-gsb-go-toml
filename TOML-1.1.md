# TOML 1.0 vs 1.1

Short overview of the differences between [TOML 1.0.0] and [TOML 1.1.0],
based on the official specifications (copies are kept in
`internal/toml-test/specs/`).

[TOML 1.0.0]: https://toml.io/en/v1.0.0
[TOML 1.1.0]: https://toml.io/en/v1.1.0

## Syntax differences

These are the new syntax features added in TOML 1.1. They are recognized by
the lexer/parser, which records them as "features" (see `CheckVersion`) or
rejects them with a version error when parsing as TOML 1.0 (see
`Decoder.Version`).

| Feature | TOML 1.0 | TOML 1.1 | Example |
| --- | --- | --- | --- |
| `\e` escape sequence | invalid escape | ESC (U+001B) | `s = "a\eb"` |
| `\xHH` escape sequence | invalid escape | U+00HH | `s = "Jos\xE9"` |
| Seconds optional in times | `07:32` invalid | `:00` assumed | `t = 07:32` |
| Newlines in inline tables | not allowed | allowed between key/value pairs | `t = {a = 1,\n b = 2}` |
| Trailing comma in inline tables | not allowed | allowed | `t = {a = 1,}` |

Since newlines are allowed inside TOML 1.1 inline tables, comments (which run
to the end of the line) are allowed there as well.

## Behavioural differences and clarifications

These are not syntax features and are not gated by the version option; they
are listed for completeness.

| Topic | TOML 1.0 | TOML 1.1 |
| --- | --- | --- |
| Newlines in multi-line literal strings | no normalization specified | parsers must normalize newlines (e.g. CRLF to LF) like multi-line basic strings |
| Multi-line strings as keys | implicitly invalid | explicitly called out as invalid |
| Dotted keys in tables | parent tables "must not have been previously created" | clarified: all key/value pairs of such a table must be defined under the current header (or root, or one inline table) |
| UTF-8 requirement | file must be valid UTF-8 | clarified as a "well-formed code-unit sequence"; reject or replace with U+FFFD |
| Escape code validity | must be Unicode scalar values | unchanged; `\xHH`, `\uHHHH`, `\UHHHHHHHH` notation in spec text |
