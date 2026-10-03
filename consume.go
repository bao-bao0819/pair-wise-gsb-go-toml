package toml

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// DeclState is the consumption state of a single TOML declaration as seen by
// a [ConsumeSession].
type DeclState uint8

const (
	// StateUntried means no component has attempted to decode the declaration
	// in this session.
	StateUntried DeclState = iota

	// StateConsumed means a decode attempt succeeded and the resulting
	// [Receipt] was accepted with [ConsumeSession.Accept].
	StateConsumed

	// StateFailed means a decode attempt read the declaration but returned an
	// error; the declaration is not considered consumed.
	StateFailed
)

// pathStep is one segment of a [DeclPath]. A non-negative arrIndex marks the
// segment as an element of an array of tables.
type pathStep struct {
	key      string
	arrIndex int
}

// DeclPath identifies a single declaration in the parsed document.
//
// Key segments name tables and values; array-of-tables elements additionally
// carry an index, so two same-named array elements have distinct identities.
// Dotted keys and ordinary table declarations resolve to the same path.
type DeclPath []pathStep

// Key returns the named segments of the path, without array indices.
func (p DeclPath) Key() []string {
	k := make([]string, len(p))
	for i, s := range p {
		k[i] = s.key
	}
	return k
}

// Index reports the array-of-tables element index of segment i. The second
// return value is false if the segment is not an array element.
func (p DeclPath) Index(i int) (int, bool) {
	if i < 0 || i >= len(p) || p[i].arrIndex < 0 {
		return 0, false
	}
	return p[i].arrIndex, true
}

// String renders the path in dotted notation, e.g. "plugins[1].config.name".
func (p DeclPath) String() string {
	var b []byte
	for i, s := range p {
		if i > 0 {
			b = append(b, '.')
		}
		b = append(b, Key{s.key}.maybeQuoted(0)...)
		if s.arrIndex >= 0 {
			b = append(b, []byte(fmt.Sprintf("[%d]", s.arrIndex))...)
		}
	}
	return string(b)
}

// encode returns a collision-free binary representation usable as a map key.
// Every step is length-prefixed, so keys containing any byte can't collide.
func (p DeclPath) encode() string {
	b := make([]byte, 0, 16)
	var n [4]byte
	for _, s := range p {
		tag := byte(0)
		if s.arrIndex >= 0 {
			tag = 1
		}
		b = append(b, tag)
		binary.BigEndian.PutUint32(n[:], uint32(len(s.key)))
		b = append(b, n[:]...)
		b = append(b, s.key...)
		if s.arrIndex >= 0 {
			binary.BigEndian.PutUint32(n[:], uint32(s.arrIndex))
			b = append(b, n[:]...)
		}
	}
	return string(b)
}

func clonePath(p DeclPath) DeclPath {
	c := make(DeclPath, len(p))
	copy(c, p)
	return c
}

// pathFromKey converts a dotted key (array indices unknown) into a DeclPath.
func pathFromKey(k Key) DeclPath {
	p := make(DeclPath, len(k))
	for i, piece := range k {
		p[i] = pathStep{key: piece, arrIndex: -1}
	}
	return p
}

// Declaration describes one declaration and how it was handled by a session.
type Declaration struct {
	// Path identifies the declaration in the source document.
	Path DeclPath

	// State is the current consumption state.
	State DeclState

	// Opaque is true when the whole subtree was taken over by a custom
	// [Unmarshaler]. Internal fields are not reported individually.
	Opaque bool

	// Reason is the error of the most recent failed attempt when State is
	// [StateFailed].
	Reason error
}

// Receipt records what one lazy decode of a [Primitive] read.
//
// A receipt is bound to the [ConsumeSession] and document that produced it and
// can't be accepted anywhere else. A failed decode still returns a receipt
// describing the attempted reads, but it can never be accepted.
type Receipt struct {
	session *ConsumeSession
	doc     uint64
	root    DeclPath
	read    []DeclPath
	opaque  bool
	err     error
}

// Root is the identity of the [Primitive] this receipt decoded.
func (r Receipt) Root() DeclPath { return r.root }

// Read lists the declarations this decode attempt touched, in document order.
// When the target implemented [Unmarshaler], only the opaque root is listed.
func (r Receipt) Read() []DeclPath {
	out := make([]DeclPath, len(r.read))
	copy(out, r.read)
	return out
}

// Failed reports whether the decode attempt returned an error.
func (r Receipt) Failed() bool { return r.err != nil }

// Err returns the error of a failed decode attempt, or nil on success.
func (r Receipt) Err() error { return r.err }

// ConsumeSession independently tracks which declarations are consumed,
// untried, or failed for one set of lazy [Primitive] decodes.
//
// Multiple sessions created from the same [MetaData] are independent: the same
// [Primitive] can be attempted by different components without interference.
type ConsumeSession struct {
	md     *MetaData
	doc    uint64
	nodes  []*declNode
	byEnc  map[string]*declNode
	states map[string]*declState
}

type declState struct {
	state  DeclState
	opaque bool
	reason error
}

// declNode is one node of the semantic declaration tree. Tables hold children
// and arrays; a leaf has neither.
type declNode struct {
	path     DeclPath
	children map[string]*declNode
	arrays   map[string][]*declNode
	order    []declChild
}

type declChild struct {
	key string
	arr bool // false: index into children; true: index into arrays
}

func newTableNode(path DeclPath) *declNode {
	return &declNode{
		path:     path,
		children: make(map[string]*declNode),
		arrays:   make(map[string][]*declNode),
	}
}

// ensureChild returns the table child named key, creating it on first use.
func (n *declNode) ensureChild(key string) *declNode {
	c, ok := n.children[key]
	if !ok {
		c = newTableNode(append(clonePath(n.path), pathStep{key: key, arrIndex: -1}))
		n.children[key] = c
		n.order = append(n.order, declChild{key: key})
	}
	return c
}

// ensureElement returns array element idx of the named array-of-tables,
// creating the array and element slots as needed.
func (n *declNode) ensureElement(key string, idx int) *declNode {
	arr := n.arrays[key]
	for len(arr) <= idx {
		path := append(clonePath(n.path), pathStep{key: key, arrIndex: len(arr)})
		arr = append(arr, newTableNode(path))
	}
	n.arrays[key] = arr
	if len(n.order) == 0 || n.order[len(n.order)-1].key != key || !n.order[len(n.order)-1].arr {
		n.order = append(n.order, declChild{key: key, arr: true})
	}
	return arr[idx]
}

// buildDeclarations constructs the identity tree from the parsed mapping and
// the ordered key list. Array element indices are assigned in source order by
// counting array-table headers.
func buildDeclarations(md *MetaData) []*declNode {
	root := newTableNode(nil)
	counts := make(map[string]int)
	for _, key := range md.keys {
		navDecl(md, root, key, counts)
	}
	fillDecl(root, md.mapping)

	var ordered []*declNode
	collectDecl(root, &ordered)
	return ordered
}

// navDecl walks one ordered key through the mapping, creating tree nodes.
func navDecl(md *MetaData, root *declNode, key Key, counts map[string]int) {
	var (
		raw  any = md.mapping
		node     = root
		path DeclPath
	)
	for i, piece := range key {
		tmap, ok := raw.(map[string]any)
		if !ok {
			return
		}
		next, exists := tmap[piece]
		if !exists {
			// Inner parts of dotted keys inside inline tables may not appear
			// in the raw map under this name; fillDecl covers those.
			return
		}
		last := i == len(key)-1
		if elems, isArr := next.([]map[string]any); isArr {
			arrPath := append(clonePath(path), pathStep{key: piece, arrIndex: -1})
			enc := arrPath.encode()
			var idx int
			if last {
				// This ordered key is the [[a.b]] header itself: allocate the
				// next element.
				idx = counts[enc]
				counts[enc] = idx + 1
			} else {
				// A key inside the most recently opened element.
				idx = counts[enc] - 1
			}
			if idx < 0 || idx >= len(elems) {
				return
			}
			node = node.ensureElement(piece, idx)
			path = clonePath(node.path)
			raw = elems[idx]
			continue
		}
		node = node.ensureChild(piece)
		path = clonePath(node.path)
		raw = next
	}
}

// fillDecl completes the tree with declarations that don't have their own
// ordered-key entries: fields inside inline tables. Newly discovered children
// are appended in sorted key order.
func fillDecl(n *declNode, raw any) {
	tmap, ok := raw.(map[string]any)
	if !ok {
		return
	}
	for _, ch := range n.order {
		if ch.arr {
			elems := tmap[ch.key].([]map[string]any)
			for i, elm := range n.arrays[ch.key] {
				if i < len(elems) {
					fillDecl(elm, elems[i])
				}
			}
		} else {
			fillDecl(n.children[ch.key], tmap[ch.key])
		}
	}

	var missing []string
	for key, val := range tmap {
		if _, ok := n.children[key]; ok {
			continue
		}
		if _, ok := n.arrays[key]; ok {
			continue
		}
		if _, ok := val.(map[string]any); ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		child := n.ensureChild(key)
		fillDecl(child, tmap[key])
	}
}

// collectDecl emits nodes in document (pre-)order.
func collectDecl(n *declNode, out *[]*declNode) {
	for _, ch := range n.order {
		if ch.arr {
			for _, elm := range n.arrays[ch.key] {
				*out = append(*out, elm)
				collectDecl(elm, out)
			}
		} else {
			c := n.children[ch.key]
			*out = append(*out, c)
			collectDecl(c, out)
		}
	}
}

var nextDocID = func() func() uint64 {
	var counter uint64
	return func() uint64 {
		counter++
		return counter
	}
}()

// consumeTracker records declarations touched by one unify run.
type consumeTracker struct {
	read   map[string]DeclPath
	opaque map[string]DeclPath
	fail   DeclPath
}

func newConsumeTracker() *consumeTracker {
	return &consumeTracker{
		read:   make(map[string]DeclPath),
		opaque: make(map[string]DeclPath),
	}
}

func (t *consumeTracker) record(p DeclPath) {
	if t == nil || len(p) == 0 {
		return
	}
	enc := p.encode()
	if _, ok := t.read[enc]; !ok {
		t.read[enc] = clonePath(p)
	}
}

func (t *consumeTracker) recordOpaque(p DeclPath) {
	if t == nil || len(p) == 0 {
		return
	}
	enc := p.encode()
	if _, ok := t.opaque[enc]; !ok {
		t.opaque[enc] = clonePath(p)
	}
	t.read[enc] = clonePath(p)
}

func (t *consumeTracker) remove(p DeclPath) {
	if t == nil {
		return
	}
	delete(t.read, p.encode())
}

// NewConsumeSession starts an independent consumption tracking session bound to
// the document that produced md.
//
// Declarations already consumed by the initial Decode (everything not hidden
// behind a [Primitive]) are seeded as consumed in the new session.
func (md *MetaData) NewConsumeSession() *ConsumeSession {
	nodes := buildDeclarations(md)
	byEnc := make(map[string]*declNode, len(nodes))
	for _, n := range nodes {
		byEnc[n.path.encode()] = n
	}
	s := &ConsumeSession{
		md:     md,
		doc:    md.docID,
		nodes:  nodes,
		byEnc:  byEnc,
		states: make(map[string]*declState),
	}

	// Seed with declarations consumed by the initial decode.
	for _, p := range md.baseRead {
		s.markConsumed(p, false)
	}
	for _, p := range md.baseOpaque {
		s.markConsumed(p, true)
	}
	return s
}

// DecodePrimitive lazily decodes primValue into v and returns a receipt of the
// declarations the attempt read.
//
// On error the declarations at the failure point are marked [StateFailed] in
// this session, but nothing is marked consumed. The caller must accept a
// successful receipt with [ConsumeSession.Accept] to mark declarations
// consumed.
func (s *ConsumeSession) DecodePrimitive(primValue Primitive, v any) (Receipt, error) {
	r := Receipt{session: s, doc: s.doc}
	if primValue.docID != s.doc {
		err := fmt.Errorf("toml: primitive belongs to document %d, but session tracks document %d",
			primValue.docID, s.doc)
		r.err = err
		r.root = clonePath(primValue.id)
		return r, err
	}
	r.root = clonePath(primValue.id)

	tracker := newConsumeTracker()
	s.md.tracker = tracker
	s.md.context = primValue.context
	defer func() {
		s.md.tracker = nil
		s.md.context = nil
		s.md.idPath = nil
	}()
	s.md.idPath = clonePath(primValue.id)

	err := s.md.unify(primValue.undecoded, rvalue(v))
	if err != nil {
		fail := tracker.fail
		if len(fail) == 0 {
			fail = clonePath(primValue.id)
		}
		r.read = orderedPaths(tracker.read, s.nodes)
		r.err = err
		s.markFailed(fail, err)
		return r, err
	}

	r.opaque = len(tracker.opaque) > 0
	r.read = orderedPaths(tracker.read, s.nodes)
	// The Primitive's own declaration is decoded by this attempt too.
	if _, ok := tracker.read[r.root.encode()]; !ok {
		r.read = append([]DeclPath{clonePath(r.root)}, r.read...)
	}
	return r, nil
}

// Accept marks the declarations of a successful receipt as consumed.
//
// It returns an error if the receipt comes from a different session or
// document, or if the decode attempt failed. Only the ledger of the session and
// array-table instance the receipt belongs to changes.
func (s *ConsumeSession) Accept(r Receipt) error {
	switch {
	case r.session != s:
		return fmt.Errorf("toml: receipt was produced by a different consume session")
	case r.doc != s.doc:
		return fmt.Errorf("toml: receipt belongs to document %d, but session tracks document %d",
			r.doc, s.doc)
	case r.err != nil:
		return fmt.Errorf("toml: cannot accept a failed receipt: %w", r.err)
	}

	opaque := r.opaque
	for _, p := range r.read {
		s.markConsumed(p, opaque)
	}
	return nil
}

// Declarations reports every declaration and its current state in document
// order.
func (s *ConsumeSession) Declarations() []Declaration {
	out := make([]Declaration, 0, len(s.nodes))
	for _, n := range s.nodes {
		st := s.states[n.path.encode()]
		d := Declaration{Path: clonePath(n.path)}
		if st != nil {
			d.State = st.state
			d.Opaque = st.opaque
			d.Reason = st.reason
		}
		out = append(out, d)
	}
	return out
}

func (s *ConsumeSession) markConsumed(p DeclPath, opaque bool) {
	enc := p.encode()
	st, ok := s.states[enc]
	if !ok {
		st = &declState{}
		s.states[enc] = st
	}
	st.state = StateConsumed
	st.reason = nil
	if opaque {
		st.opaque = true
		s.markSubtreeConsumed(p)
	}
}

// markSubtreeConsumed marks every declaration below an opaque [Unmarshaler]
// root as consumed; internal reads are not guessed.
func (s *ConsumeSession) markSubtreeConsumed(root DeclPath) {
	node, ok := s.byEnc[root.encode()]
	if !ok {
		return
	}
	var descendants []*declNode
	collectDecl(node, &descendants)
	for _, d := range descendants {
		enc := d.path.encode()
		st, ok := s.states[enc]
		if !ok {
			st = &declState{}
			s.states[enc] = st
		}
		if st.state != StateFailed {
			st.state = StateConsumed
			st.reason = nil
		}
		st.opaque = true
	}
}

func (s *ConsumeSession) markFailed(p DeclPath, reason error) {
	enc := p.encode()
	st, ok := s.states[enc]
	if !ok {
		st = &declState{}
		s.states[enc] = st
	}
	if st.state == StateConsumed {
		return
	}
	st.state = StateFailed
	st.opaque = false
	st.reason = reason
}

// orderedPaths returns the recorded paths in document order.
func orderedPaths(set map[string]DeclPath, nodes []*declNode) []DeclPath {
	wanted := make(map[string]struct{}, len(set))
	for enc := range set {
		wanted[enc] = struct{}{}
	}
	out := make([]DeclPath, 0, len(set))
	for _, n := range nodes {
		enc := n.path.encode()
		if _, ok := wanted[enc]; ok {
			out = append(out, clonePath(n.path))
			delete(wanted, enc)
		}
	}
	// Paths that don't appear in the tree (e.g. the virtual root) come last.
	for _, p := range set {
		if _, ok := wanted[p.encode()]; ok {
			out = append(out, clonePath(p))
		}
	}
	return out
}
