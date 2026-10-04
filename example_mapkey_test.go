package toml_test

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Region is an enum used as a map key.
type Region int

const (
	RegionEUWest Region = iota
	RegionUSEast
)

func (r Region) String() string {
	switch r {
	case RegionEUWest:
		return "eu-west"
	case RegionUSEast:
		return "us-east"
	}
	return "unknown"
}

// ParseRegion is the inverse of Region.String.
func ParseRegion(s string) (Region, error) {
	switch s {
	case "eu-west":
		return RegionEUWest, nil
	case "us-east":
		return RegionUSEast, nil
	}
	return 0, fmt.Errorf("unknown region %q", s)
}

type Endpoint struct {
	Host string `toml:"host"`
	TLS  bool   `toml:"tls"`
}

// Maps with a non-string key type such as an enum can be encoded and decoded
// by registering a MapKeyCodec for the key type.
func ExampleNewMapKeyCodec() {
	codec := toml.NewMapKeyCodec(
		func(r Region) (string, error) { return r.String(), nil },
		ParseRegion,
	)

	cfg := map[Region]Endpoint{
		RegionEUWest: {Host: "eu.example.com", TLS: true},
		RegionUSEast: {Host: "us.example.com", TLS: false},
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.MapKeyCodecs = []toml.MapKeyCodec{codec}
	if err := enc.Encode(cfg); err != nil {
		panic(err)
	}
	fmt.Print(buf.String())

	var roundTrip map[Region]Endpoint
	dec := toml.NewDecoder(strings.NewReader(buf.String()))
	dec.MapKeyCodecs = []toml.MapKeyCodec{codec}
	if _, err := dec.Decode(&roundTrip); err != nil {
		panic(err)
	}
	fmt.Println("round trip:", roundTrip[RegionEUWest].Host, roundTrip[RegionUSEast].Host)

	// Output:
	// [eu-west]
	//   host = "eu.example.com"
	//   tls = true
	//
	// [us-east]
	//   host = "us.example.com"
	//   tls = false
	// round trip: eu.example.com us.example.com
}
