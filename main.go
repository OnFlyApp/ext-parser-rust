package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const (
	// The host cancels a parse after 5 s, and a cancelled file keeps its stale
	// chunks. A deadline inside the plugin is not an option: the host does not
	// enable wazero's real clocks, so time.Now() is fake there. Big files are
	// therefore not parsed; they become plain `file` chunks.
	maxParseSize    = 80 << 10
	maxFallbackSize = 2 << 20 // above this the file is skipped (generated/vendored)
	maxChunkSize    = 8 << 10 // longer function bodies are split at line boundaries
)

// Chunk is the universal contract defined in the Onfly SDD.
type Chunk struct {
	FilePath string            `json:"file_path"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata"`
}

// fatalError writes to stderr so the Onfly core can log it without
// breaking the stdout JSON pipeline.
func fatalError(err error) {
	fmt.Fprintf(os.Stderr, "onfly-rust-parser error: %v\n", err)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fatalError(fmt.Errorf("missing file path argument"))
	}
	if os.Args[1] == "--batch" {
		runBatch()
		return
	}
	filePath := os.Args[1]
	src, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatalError(fmt.Errorf("failed to read stdin: %w", err))
	}
	chunks, err := parse(filePath, src)
	if err != nil {
		fatalError(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(chunks); err != nil {
		fatalError(fmt.Errorf("failed to encode json: %w", err))
	}
}

type walker struct {
	src    []byte
	lang   *gotreesitter.Language
	file   string
	lines  []int // byte offset of each line start
	chunks []Chunk
}

func parse(filePath string, src []byte) (chunks []Chunk, err error) {
	defer func() {
		if r := recover(); r != nil {
			chunks, err = nil, fmt.Errorf("parser panic: %v", r)
		}
	}()
	// Initialized empty so JSON serializes as [] instead of null.
	chunks = []Chunk{}
	if len(src) == 0 {
		return chunks, nil
	}
	w := &walker{src: src, file: filePath, lines: []int{0}, chunks: chunks}
	for i, b := range src {
		if b == '\n' {
			w.lines = append(w.lines, i+1)
		}
	}
	switch {
	case len(src) > maxFallbackSize:
		fmt.Fprintf(os.Stderr, "onfly-rust-parser: %s is %d bytes, skipped\n", filePath, len(src))
		return chunks, nil
	case len(src) > maxParseSize:
		return w.wholeFile("too_large"), nil
	}
	w.lang = grammars.RustLanguage()
	tree, err := gotreesitter.NewParser(w.lang).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rust file: %w", err)
	}
	root := tree.RootNode()
	if root.HasError() {
		fmt.Fprintf(os.Stderr, "onfly-rust-parser: %s has syntax errors, chunking what parsed\n", filePath)
	}
	w.block(root, "")
	if len(w.chunks) == 0 {
		return w.wholeFile(""), nil
	}
	return w.chunks, nil
}

// wholeFile returns the file as `file` chunks (split by emit at line boundaries).
func (w *walker) wholeFile(reason string) []Chunk {
	meta := map[string]string{"name": w.file}
	if reason != "" {
		meta["reason"] = reason
	}
	w.emit("file", 0, uint32(len(w.src)), meta)
	return w.chunks
}

// ---- helpers ----

func (w *walker) typ(n *gotreesitter.Node) string { return n.Type(w.lang) }
func (w *walker) text(n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}
	return string(w.src[n.StartByte():n.EndByte()])
}
func (w *walker) field(n *gotreesitter.Node, name string) *gotreesitter.Node {
	return n.ChildByFieldName(name, w.lang)
}

// lead widens a node's start over the doc comments, comments and attributes
// directly above it (no blank line between, each on a line of its own) and
// returns the attributes. `//!` inner docs belong to the enclosing module.
func (w *walker) lead(n *gotreesitter.Node) (uint32, []string) {
	start := n.StartByte()
	var attrs []string
	for p := n.PrevSibling(); p != nil; p = p.PrevSibling() {
		t := w.typ(p)
		if t != "line_comment" && t != "block_comment" && t != "attribute_item" {
			break
		}
		if tx := w.text(p); strings.HasPrefix(tx, "//!") || strings.HasPrefix(tx, "/*!") {
			break
		}
		if strings.Count(string(w.src[p.EndByte():start]), "\n") > 1 {
			break
		}
		ls := w.lines[w.line(p.StartByte())-1]
		if strings.TrimSpace(string(w.src[ls:p.StartByte()])) != "" {
			break
		}
		if t == "attribute_item" {
			attrs = append([]string{w.text(p)}, attrs...)
		}
		start = p.StartByte()
	}
	return start, attrs
}

func (w *walker) line(off uint32) int {
	return sort.Search(len(w.lines), func(i int) bool { return w.lines[i] > int(off) })
}

func (w *walker) emit(typ string, start, end uint32, meta map[string]string) {
	for end > start && strings.TrimSpace(string(w.src[end-1:end])) == "" {
		end--
	}
	if end <= start {
		return
	}
	meta["start_line"] = strconv.Itoa(w.line(start))
	meta["end_line"] = strconv.Itoa(w.line(end - 1))
	content := string(w.src[start:end])
	if len(content) <= maxChunkSize || typ == "trait" || typ == "impl" || typ == "module" {
		w.chunks = append(w.chunks, Chunk{FilePath: w.file, Type: typ, Content: content, Metadata: meta})
		return
	}
	var parts []string
	for len(content) > maxChunkSize {
		cut := strings.LastIndex(content[:maxChunkSize], "\n")
		if cut <= 0 {
			cut = maxChunkSize
			for cut > 0 && !utf8.RuneStart(content[cut]) {
				cut--
			}
		}
		parts = append(parts, content[:cut])
		content = content[cut:]
	}
	parts = append(parts, content)
	for i, p := range parts {
		m := make(map[string]string, len(meta)+1)
		for k, v := range meta {
			m[k] = v
		}
		m["part"] = fmt.Sprintf("%d/%d", i+1, len(parts))
		w.chunks = append(w.chunks, Chunk{FilePath: w.file, Type: typ, Content: p, Metadata: m})
	}
}

func join(scope, name string) string {
	if scope == "" {
		return name
	}
	return scope + "::" + name
}

// ---- walk ----

var itemKinds = map[string]string{
	"struct_item": "struct", "enum_item": "enum", "union_item": "union", "type_item": "type",
	"const_item": "constant", "static_item": "static", "macro_definition": "macro",
}

// block walks the items of the file or of an inline module body. mod is the
// enclosing module path, empty at the crate root of the file.
func (w *walker) block(parent *gotreesitter.Node, mod string) {
	var run []*gotreesitter.Node
	flush := func() {
		if len(run) == 0 {
			return
		}
		first, last := run[0], run[len(run)-1]
		start, _ := w.lead(first)
		w.emit("statement", start, last.EndByte(), map[string]string{"name": "statements", "namespace": mod})
		run = nil
	}
	for i := 0; i < parent.ChildCount(); i++ {
		c := parent.Child(i)
		switch t := w.typ(c); t {
		case "line_comment", "block_comment", "attribute_item", "inner_attribute_item", "{", "}":
		case "use_declaration":
			flush()
			w.emit("use", c.StartByte(), c.EndByte(), map[string]string{"name": strings.Join(strings.Fields(w.text(c)), " "), "namespace": mod})
		case "function_item":
			flush()
			w.function(c, mod, "")
		case "trait_item":
			flush()
			w.trait(c, mod)
		case "impl_item":
			flush()
			w.impl(c, mod)
		case "mod_item":
			flush()
			w.module(c, mod)
		default:
			if kind, ok := itemKinds[t]; ok {
				flush()
				w.item(c, kind, mod, "")
				continue
			}
			run = append(run, c)
		}
	}
	flush()
}

// meta fills what every declaration carries: name, fqn, namespace, visibility,
// attributes and generics. receiver is the owning type, empty for free items.
func (w *walker) meta(n *gotreesitter.Node, mod, receiver string, attrs []string) map[string]string {
	name := w.text(w.field(n, "name"))
	m := map[string]string{"name": name, "namespace": mod}
	if receiver != "" {
		m["receiver"] = receiver
		m["fqn"] = receiver + "::" + name
	} else {
		m["fqn"] = join(mod, name)
	}
	if len(attrs) > 0 {
		m["attributes"] = strings.Join(attrs, " ")
	}
	for i := 0; i < n.ChildCount(); i++ {
		if c := n.Child(i); w.typ(c) == "visibility_modifier" {
			m["visibility"] = w.text(c)
		}
	}
	if p := w.field(n, "type_parameters"); p != nil {
		m["type_params"] = w.text(p)
	}
	return m
}

// item emits a declaration whole: struct, enum, union, type alias, const, static, macro_rules.
func (w *walker) item(n *gotreesitter.Node, kind, mod, receiver string) {
	start, attrs := w.lead(n)
	m := w.meta(n, mod, receiver, attrs)
	if t := w.field(n, "type"); t != nil && (kind == "constant" || kind == "static" || kind == "type") {
		m["type"] = w.text(t)
	}
	w.emit(kind, start, n.EndByte(), m)
}

func (w *walker) function(n *gotreesitter.Node, mod, receiver string) {
	start, attrs := w.lead(n)
	m := w.meta(n, mod, receiver, attrs)
	end := n.EndByte()
	body := w.field(n, "body")
	if body != nil {
		end = body.StartByte()
	} else {
		m["bodyless"] = "true" // trait method without a default body
	}
	m["signature"] = strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(string(w.src[n.StartByte():end])), ";")), " ")
	if p := w.field(n, "parameters"); p != nil {
		m["params"] = w.text(p)
	}
	if r := w.field(n, "return_type"); r != nil {
		m["return_type"] = w.text(r)
	}
	for i := 0; i < n.ChildCount(); i++ {
		if c := n.Child(i); w.typ(c) == "function_modifiers" {
			m["modifiers"] = strings.Join(strings.Fields(w.text(c)), " ")
		}
	}
	typ := "function"
	if receiver != "" {
		typ = "method"
	}
	w.emit(typ, start, n.EndByte(), m)
}

// module emits `mod x;` whole, or the header of `mod x { ... }` and then its items.
func (w *walker) module(n *gotreesitter.Node, mod string) {
	start, attrs := w.lead(n)
	m := w.meta(n, mod, "", attrs)
	body := w.field(n, "body")
	if body == nil {
		w.emit("module", start, n.EndByte(), m)
		return
	}
	w.emit("module", start, body.StartByte(), m)
	w.block(body, m["fqn"])
}

// trait emits the header and then the members, which point back through `receiver`.
func (w *walker) trait(n *gotreesitter.Node, mod string) {
	start, attrs := w.lead(n)
	m := w.meta(n, mod, "", attrs)
	if b := w.field(n, "bounds"); b != nil {
		m["extends"] = strings.TrimSpace(strings.TrimPrefix(w.text(b), ":"))
	}
	body := w.field(n, "body")
	if body == nil {
		w.emit("trait", start, n.EndByte(), m)
		return
	}
	w.emit("trait", start, body.StartByte(), m)
	w.members(body, mod, m["fqn"])
}

// impl emits the header (`impl<T> Tr for S<T>`) and then its members with
// receiver = the implemented type.
func (w *walker) impl(n *gotreesitter.Node, mod string) {
	start, attrs := w.lead(n)
	ty := w.field(n, "type")
	if ty == nil {
		return
	}
	base := ty
	if w.typ(ty) == "generic_type" {
		if b := w.field(ty, "type"); b != nil {
			base = b
		}
	}
	recv := join(mod, w.text(base))
	m := map[string]string{"name": strings.Join(strings.Fields(w.text(ty)), " "), "namespace": mod, "type": w.text(ty)}
	if tr := w.field(n, "trait"); tr != nil {
		m["trait"] = w.text(tr)
		m["name"] = strings.Join(strings.Fields(w.text(tr)+" for "+w.text(ty)), " ")
	}
	if len(attrs) > 0 {
		m["attributes"] = strings.Join(attrs, " ")
	}
	if p := w.field(n, "type_parameters"); p != nil {
		m["type_params"] = w.text(p)
	}
	body := w.field(n, "body")
	if body == nil {
		w.emit("impl", start, n.EndByte(), m)
		return
	}
	w.emit("impl", start, body.StartByte(), m)
	w.members(body, mod, recv)
}

func (w *walker) members(body *gotreesitter.Node, mod, receiver string) {
	for i := 0; i < body.ChildCount(); i++ {
		c := body.Child(i)
		switch t := w.typ(c); t {
		case "function_item", "function_signature_item":
			w.function(c, mod, receiver)
		case "const_item":
			w.item(c, "constant", mod, receiver)
		case "type_item", "associated_type":
			w.item(c, "type", mod, receiver)
		}
	}
}
