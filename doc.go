// Package toml implements decoding and encoding of TOML files.
//
// This package supports TOML v1.1.0, as specified at https://toml.io
//
// Documents can also be parsed as TOML v1.0.0 with [Decoder.Version], in
// which case any TOML 1.1 feature is reported as an error. [CheckVersion]
// reports the minimum TOML version a document requires, listing every TOML
// 1.1 feature it uses.
//
// The github.com/BurntSushi/toml/cmd/tomlv package implements a TOML validator,
// and can be used to verify if TOML document is valid. It can also be used to
// print the type of each key.
package toml
