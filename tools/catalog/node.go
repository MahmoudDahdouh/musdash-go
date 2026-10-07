package main

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// The Compose files are changed as trees of yaml.v3 nodes. A node keeps a
// scalar as it was written (its text and its quoting), so a value this
// program does not touch comes out as it went in: 0755 stays 0755 and "yes"
// stays "yes".

func isMap(n *yaml.Node) bool { return n != nil && n.Kind == yaml.MappingNode }
func isSeq(n *yaml.Node) bool { return n != nil && n.Kind == yaml.SequenceNode }
func isStr(n *yaml.Node) bool { return n != nil && n.Kind == yaml.ScalarNode }

func str(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if strings.Contains(s, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

func newMap() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }
func newSeq() *yaml.Node { return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"} }

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if !isMap(m) {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, str(key), v)
}

func mapDel(m *yaml.Node, key string) *yaml.Node {
	if !isMap(m) {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return v
		}
	}
	return nil
}

func mapKeys(m *yaml.Node) []string {
	if !isMap(m) {
		return nil
	}
	keys := make([]string, 0, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		keys = append(keys, m.Content[i].Value)
	}
	return keys
}

// isNull reports a value that was left out: "key:" with nothing after it.
func isNull(n *yaml.Node) bool {
	return n == nil || n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func truthy(n *yaml.Node) bool {
	return isStr(n) && strings.EqualFold(n.Value, "true")
}

func clone(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = clone(child)
	}
	return &c
}

// expand writes out every alias and every "<<" merge, so that the rest of
// the program sees each service whole.
func expand(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		return expand(clone(n.Alias))
	}
	n.Anchor = ""
	for i, child := range n.Content {
		n.Content[i] = expand(child)
	}
	if n.Kind != yaml.MappingNode {
		return n
	}
	var merged []*yaml.Node
	own := n.Content[:0:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value != "<<" || k.Tag == "!!str" {
			own = append(own, k, v)
			continue
		}
		if isMap(v) {
			merged = append(merged, v)
		} else if isSeq(v) {
			merged = append(merged, v.Content...)
		}
	}
	if merged == nil {
		return n
	}
	// "<<" on a list, which some templates write to repeat one: the list
	// itself is what was meant.
	if len(own) == 0 && !isMap(merged[0]) {
		seq := newSeq()
		for _, m := range merged {
			seq.Content = append(seq.Content, clone(m))
		}
		return seq
	}
	n.Content = own
	for _, m := range merged {
		for _, key := range mapKeys(m) {
			if mapGet(n, key) == nil {
				n.Content = append(n.Content, str(key), clone(mapGet(m, key)))
			}
		}
	}
	return n
}

// scalars calls fn for every scalar value under n, and for every key.
func scalars(n *yaml.Node, fn func(*yaml.Node)) {
	if n == nil {
		return
	}
	if n.Kind == yaml.ScalarNode {
		fn(n)
		return
	}
	for _, child := range n.Content {
		scalars(child, fn)
	}
}

// values calls fn for every scalar under n that is not a mapping's key.
func values(n *yaml.Node, fn func(*yaml.Node)) {
	switch {
	case n == nil:
	case n.Kind == yaml.ScalarNode:
		fn(n)
	case n.Kind == yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			values(n.Content[i], fn)
		}
	default:
		for _, child := range n.Content {
			values(child, fn)
		}
	}
}

// setValue changes a scalar's text. A plain scalar that would now be read
// as something other than text is quoted.
func setValue(n *yaml.Node, s string) {
	if n.Value == s {
		return
	}
	n.Value = s
	if n.Tag != "!!str" {
		n.Tag = "!!str"
	}
	if strings.Contains(s, "\n") && n.Style != yaml.LiteralStyle && n.Style != yaml.FoldedStyle {
		n.Style = yaml.LiteralStyle
	}
}
