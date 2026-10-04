// Package toml implements decoding and encoding of TOML files.
//
// This package supports TOML v1.1.0, as specified at https://toml.io, and
// parses documents as TOML 1.1 by default. Use [Decoder.Version] to parse
// documents as an older TOML version, and [CheckVersion] to find the minimum
// TOML version a document requires.
//
// The github.com/BurntSushi/toml/cmd/tomlv package implements a TOML validator,
// and can be used to verify if TOML document is valid. It can also be used to
// print the type of each key.
package toml
