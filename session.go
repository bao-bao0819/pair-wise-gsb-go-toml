package toml

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// DeclState describes how a single declaration in a TOML document relates to
// the consumers of that document, as tracked by a [Session].
type DeclState int

const (
	// DeclPending means the declaration has not been consumed: no decode
	// attempt read it, or a deferred [Primitive] holding it was never
	// decoded (or its receipt was never accepted).
	DeclPending DeclState = iota

	// DeclConsumed means the declaration was read by a decode: either by
	// the initial Decode call, or by a [Session] decode whose [Receipt]
	// was explicitly accepted.
	DeclConsumed

	// DeclFailed means a decode attempt read the declaration but failed.
	// The failure reason is available on [Declaration.Err]. A failed
	// declaration is not consumed; a later successful attempt (with an
	// accepted receipt) moves it to [DeclConsumed].
	DeclFailed
)

func (s DeclState) String() string {
	switch s {
	case DeclPending:
		return "pending"
	case DeclConsumed:
		return "consumed"
	case DeclFailed:
		return "failed"
	}
	return "unknown"
}

// Declaration reports the state of one declaration in the tracked document.
//
// The Path identifies the declaration; elements of tables arrays are
// addressed by index, for example `plugins[1].name`. Dotted keys and table
// headers describing the same value share a single declaration, so `a.b = 1`
// and `[a] b = 1` both produce the declaration `a.b`.
type Declaration struct {
	Path  string
	State DeclState

	// Err is the reason the last decode attempt failed, if State is
	// DeclFailed.
	Err error
}

// declNode is one declaration in the document tree built at parse time.
// Array-of-tables elements are nodes with index >= 0 and no name.
type declNode struct {
	session  *Session
	parent   *declNode
	name     string // empty for the root and for array elements
	index    int    // element index for array-of-tables elements, -1 otherwise
	seq      int    // creation order; approximates document order
	children map[string]*declNode
	order    []*declNode // named children, in order of first appearance
	elems    []*declNode // array-of-tables elements

	state DeclState
	err   error
}

func (n *declNode) path() string {
	switch {
	case n.parent == nil:
		return ""
	case n.index >= 0:
		return n.parent.path() + "[" + strconv.Itoa(n.index) + "]"
	default:
		p := n.parent.path()
		q := Key{n.name}.maybeQuoted(0)
		if p == "" {
			return q
		}
		return p + "." + q
	}
}

func (n *declNode) child(name string) *declNode {
	if n.children == nil {
		n.children = make(map[string]*declNode)
	}
	if c, ok := n.children[name]; ok {
		return c
	}
	c := &declNode{session: n.session, parent: n, name: name, index: -1, seq: n.session.nextSeq()}
	n.children[name] = c
	n.order = append(n.order, c)
	return c
}

func (n *declNode) element(idx int) *declNode {
	for len(n.elems) <= idx {
		n.elems = append(n.elems, nil)
	}
	if n.elems[idx] == nil {
		n.elems[idx] = &declNode{session: n.session, parent: n, index: idx, seq: n.session.nextSeq()}
	}
	return n.elems[idx]
}

func (n *declNode) consumeSubtree() {
	n.state = DeclConsumed
	n.err = nil
	for _, c := range n.children {
		c.consumeSubtree()
	}
	for _, e := range n.elems {
		e.consumeSubtree()
	}
}

func (n *declNode) addSubtree(set map[*declNode]struct{}) {
	set[n] = struct{}{}
	for _, c := range n.children {
		c.addSubtree(set)
	}
	for _, e := range n.elems {
		e.addSubtree(set)
	}
}

// Session tracks which declarations of one decoded TOML document were
// consumed, which were never attempted, and which decode attempts failed.
//
// A session is created by [Decoder.Decode] and is available through
// [MetaData.Session]. The initial decode marks every declaration it reads as
// consumed. Declarations hidden behind a [Primitive] stay pending until the
// Primitive is decoded through the session and the returned [Receipt] is
// accepted:
//
//	receipt, err := md.Session().Decode(cfg.Plugins[0], &plugin)
//	if err != nil {
//		// The attempt failed; the declarations it read are marked as
//		// failed and receipt.Err() reports why. Nothing was consumed.
//	}
//	receipt.Accept() // Only now are the read declarations consumed.
//
// Decoding the same Primitive multiple times is allowed; every attempt is
// independent and gets its own receipt. A scope taken over by a custom
// [Unmarshaler] is marked consumed as a whole, since the session cannot know
// which fields it read.
//
// The session is independent from [MetaData.Undecoded]: neither
// [Session.Decode] nor [Receipt.Accept] change what Undecoded reports.
type Session struct {
	root    *declNode
	keyInfo map[string]keyInfo
	data    []byte
	seq     int
}

func (s *Session) nextSeq() int {
	s.seq++
	return s.seq
}

// newSession builds the declaration tree for a parsed document. Dotted keys
// and table headers converge on the same nodes because the tree mirrors the
// parsed mapping; array-of-tables elements become indexed child nodes.
func newSession(mapping map[string]any, ordered []Key, keyInfo map[string]keyInfo, data []byte) *Session {
	s := &Session{keyInfo: keyInfo, data: data}
	s.root = &declNode{session: s, index: -1}

	var (
		// Array-of-tables path → element index that following keys belong
		// to, mirroring the parser's "most recent table" semantics.
		cursor = make(map[string]int)
		// Array-of-tables path → number of [[...]] headers seen for it
		// within the current parent element.
		headers = make(map[string]int)
	)
	for _, k := range ordered {
		var (
			node       = s.root
			val    any = mapping
			prefix     = make(Key, 0, len(k))
		)
		for i, part := range k {
			m, ok := val.(map[string]any)
			if !ok {
				break
			}
			child := node.child(part)
			prefix = append(prefix, part)
			switch tv := m[part].(type) {
			case []map[string]any:
				ps := prefix.String()
				var idx int
				if i == len(k)-1 {
					// A [[...]] header for this path: it appended a
					// new element, and nested arrays start over.
					idx = headers[ps]
					headers[ps] = idx + 1
					cursor[ps] = idx
					for key := range cursor {
						if key != ps && strings.HasPrefix(key, ps+".") {
							delete(cursor, key)
						}
					}
					for key := range headers {
						if key != ps && strings.HasPrefix(key, ps+".") {
							delete(headers, key)
						}
					}
				} else {
					idx = cursor[ps]
				}
				if idx >= len(tv) {
					val = nil
				} else {
					child = child.element(idx)
					val = tv[idx]
				}
			case map[string]any:
				val = tv
			default:
				val = nil
			}
			node = child
		}
	}
	return s
}

// Decode decodes a [Primitive] obtained when decoding this session's
// document, like [MetaData.PrimitiveDecode], and returns a [Receipt]
// describing the declarations the attempt read.
//
// On success nothing is consumed yet: the caller must explicitly accept the
// receipt to mark the declarations as consumed. On failure the receipt keeps
// the reason and the declarations it read are marked as failed, not
// consumed.
//
// The Primitive must belong to this session's document; decoding a Primitive
// from another document — even one with identical contents — is an error.
func (s *Session) Decode(prim Primitive, v any) (*Receipt, error) {
	r := &Receipt{session: s, anchor: prim.node}
	if prim.node == nil || prim.node.session != s {
		r.err = errors.New("toml: Primitive does not belong to this session's document")
		return r, r.err
	}

	md := &MetaData{
		keyInfo: s.keyInfo,
		decoded: map[string]struct{}{},
		data:    s.data,
		context: append(Key{}, prim.context...),
		node:    prim.node,
		session: s,
		readSet: map[*declNode]struct{}{prim.node: {}},
	}
	err := md.unify(prim.undecoded, rvalue(v))

	r.decls = make([]*declNode, 0, len(md.readSet))
	for n := range md.readSet {
		r.decls = append(r.decls, n)
	}
	sort.Slice(r.decls, func(i, j int) bool { return r.decls[i].seq < r.decls[j].seq })

	if err != nil {
		r.err = err
		for _, n := range r.decls {
			if n.state != DeclConsumed {
				n.state = DeclFailed
				n.err = err
			}
		}
		return r, err
	}
	return r, nil
}

// Declarations returns the state of every declaration in the document, in
// document order.
func (s *Session) Declarations() []Declaration {
	var out []Declaration
	var walk func(n *declNode)
	walk = func(n *declNode) {
		if n.parent != nil {
			out = append(out, Declaration{Path: n.path(), State: n.state, Err: n.err})
		}
		for _, c := range n.order {
			walk(c)
		}
		for _, e := range n.elems {
			walk(e)
		}
	}
	walk(s.root)
	return out
}

// Receipt describes the declarations read by a single [Session.Decode]
// attempt. It is bound to the document and to the Primitive the attempt
// decoded, and cannot be applied to another document.
type Receipt struct {
	session  *Session
	anchor   *declNode
	decls    []*declNode
	err      error
	accepted bool
}

// Err returns the reason the decode attempt failed, or nil if it succeeded.
// A failed receipt cannot be accepted.
func (r *Receipt) Err() error { return r.err }

// Declarations returns the declarations the decode attempt read, in document
// order, with their current state in the session.
func (r *Receipt) Declarations() []Declaration {
	out := make([]Declaration, 0, len(r.decls))
	for _, n := range r.decls {
		out = append(out, Declaration{Path: n.path(), State: n.state, Err: n.err})
	}
	return out
}

// Accept marks every declaration the decode attempt read as consumed in the
// session. It returns an error if the decode attempt failed or if the
// receipt was already accepted.
func (r *Receipt) Accept() error {
	if r.err != nil {
		return fmt.Errorf("toml: cannot accept a failed receipt: %w", r.err)
	}
	if r.accepted {
		return errors.New("toml: receipt already accepted")
	}
	r.accepted = true
	for _, n := range r.decls {
		n.state = DeclConsumed
		n.err = nil
	}
	return nil
}
