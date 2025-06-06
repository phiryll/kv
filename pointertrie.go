package kv

import (
	"bytes"
	"fmt"
	"iter"
	"strings"
)

//nolint:govet
type ptrTrieNode[V any] struct {
	children []*ptrTrieNode[V]
	root     Optional[V]
	keyByte  byte
}

// NewPointerTrie returns a new, absurdly simple, and badly coded Store.
// Pointers to children are stored densely in slices.
// This is purely for fleshing out the unit tests, benchmarks, and fuzz tests.
//
//nolint:iface
func NewPointerTrie[V any]() Store[V] {
	return &ptrTrieNode[V]{}
}

func (n *ptrTrieNode[V]) Get(key []byte) (V, bool) {
	if key == nil {
		panic("key must be non-nil")
	}
	var zero V
	for _, keyByte := range key {
		index, found := n.search(keyByte)
		if !found {
			return zero, false
		}
		n = n.children[index]
	}
	// n = found key
	return n.root.Get()
}

func (n *ptrTrieNode[V]) Set(key []byte, value V) (V, bool) {
	if key == nil {
		panic("key must be non-nil")
	}
	var zero V
	for i, keyByte := range key {
		index, found := n.search(keyByte)
		if !found {
			k := len(key) - 1
			child := &ptrTrieNode[V]{nil, OptionalOf(value), key[k]}
			for k--; k >= i; k-- {
				child = &ptrTrieNode[V]{[]*ptrTrieNode[V]{child}, Optional[V]{}, key[k]}
			}
			n.children = append(n.children, child)
			copy(n.children[index+1:], n.children[index:])
			n.children[index] = child
			return zero, false
		}
		n = n.children[index]
	}
	// n = found key, replace value
	return n.root.Set(value)
}

func (n *ptrTrieNode[V]) Delete(key []byte) (V, bool) {
	if key == nil {
		panic("key must be non-nil")
	}
	var zero V
	// If the deleted node has no children, remove the subtree rooted at prune.children[pruneIndex].
	var prune *ptrTrieNode[V]
	var pruneIndex int
	for i, keyByte := range key {
		index, found := n.search(keyByte)
		if !found {
			return zero, false
		}
		// If either n is the root, or n has a value, or n has more than one child, then n itself cannot be pruned.
		// If so, move the maybe-pruned subtree to n.children[index].
		if i == 0 || !n.root.IsEmpty() || len(n.children) > 1 {
			prune, pruneIndex = n, index
		}
		n = n.children[index]
	}
	// n = found key
	value, ok := n.root.Clear()
	if ok && len(key) > 0 && len(n.children) == 0 {
		children := prune.children
		copy(children[pruneIndex:], children[pruneIndex+1:])
		children[len(children)-1] = nil
		prune.children = children[:len(children)-1]
	}
	return value, ok
}

// An iter.Seq of these is returned from the adjFunction used internally by Range.
// key = path from root to node
// It is cached here for efficiency, otherwise an iter.Seq of []*ptrTrieNode[V] would be used directly.
// Note that the key must be cloned when yielded from Range.
type ptrTrieRangePath[V any] struct {
	node *ptrTrieNode[V]
	key  []byte
}

func (n *ptrTrieNode[V]) Range(bounds *Bounds) iter.Seq2[[]byte, V] {
	bounds = bounds.Clone()
	root := ptrTrieRangePath[V]{n, []byte{}}
	var pathItr iter.Seq[*ptrTrieRangePath[V]]
	if bounds.IsReverse {
		pathItr = postOrder(&root, ptrTrieReverseAdj[V](bounds))
	} else {
		pathItr = preOrder(&root, ptrTrieForwardAdj[V](bounds))
	}
	return func(yield func([]byte, V) bool) {
		for path := range pathItr {
			cmp := bounds.CompareKey(path.key)
			if cmp < 0 {
				continue
			}
			if cmp > 0 {
				return
			}
			if value, ok := path.node.root.Get(); ok && !yield(bytes.Clone(path.key), value) {
				return
			}
		}
	}
}

func ptrTrieForwardAdj[V any](bounds *Bounds) adjFunction[*ptrTrieRangePath[V]] {
	// Sometimes a child is not within the bounds, but one of its descendants is.
	return func(path *ptrTrieRangePath[V]) iter.Seq[*ptrTrieRangePath[V]] {
		if len(path.node.children) == 0 {
			return emptySeq
		}
		start, stop, ok := bounds.childBounds(path.key)
		if !ok {
			// Unreachable because of how the trie is traversed forward.
			panic("unreachable")
		}
		return func(yield func(*ptrTrieRangePath[V]) bool) {
			for _, child := range path.node.children {
				keyByte := child.keyByte
				if keyByte < start {
					continue
				}
				if keyByte > stop {
					return
				}
				if !yield(&ptrTrieRangePath[V]{child, append(path.key, keyByte)}) {
					return
				}
			}
		}
	}
}

func ptrTrieReverseAdj[V any](bounds *Bounds) adjFunction[*ptrTrieRangePath[V]] {
	// Sometimes a child is not within the bounds, but one of its descendants is.
	return func(path *ptrTrieRangePath[V]) iter.Seq[*ptrTrieRangePath[V]] {
		if len(path.node.children) == 0 {
			return emptySeq
		}
		start, stop, ok := bounds.childBounds(path.key)
		if !ok {
			return emptySeq
		}
		return func(yield func(*ptrTrieRangePath[V]) bool) {
			for i := len(path.node.children) - 1; i >= 0; i-- {
				child := path.node.children[i]
				keyByte := child.keyByte
				if keyByte > start {
					continue
				}
				if keyByte < stop {
					return
				}
				if !yield(&ptrTrieRangePath[V]{child, append(path.key, keyByte)}) {
					return
				}
			}
		}
	}
}

func (n *ptrTrieNode[V]) search(byt byte) (int, bool) {
	// Copied and tweaked from sort.Search. Inlining this is much, much faster.
	// Invariant: child[i-1] < byt <= child[j]
	i, j := 0, len(n.children)
	for i < j {
		//nolint:gosec
		h := int(uint(i+j) >> 1) // avoid overflow when computing h
		// i ≤ h < j
		childByte := n.children[h].keyByte
		if childByte == byt {
			return h, true
		}
		if childByte < byt {
			i = h + 1 // preserves child[i-1] < byt
		} else {
			j = h // preserves byt <= child[j]
		}
	}
	return i, false
}

func (n *ptrTrieNode[V]) String() string {
	var s strings.Builder
	s.WriteString("{")
	if value, ok := n.root.Get(); ok {
		fmt.Fprintf(&s, ":%v, ", value)
	}
	for _, child := range n.children {
		fmt.Fprintf(&s, "%02X:%s, ", child.keyByte, child)
	}
	s.WriteString("}")
	return s.String()
}
