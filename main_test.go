package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func kinds(t *testing.T, src string) map[string]Chunk {
	t.Helper()
	chunks, err := parse("src/x.rs", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Chunk{}
	for _, c := range chunks {
		key := c.Type + " " + c.Metadata["fqn"]
		if c.Metadata["fqn"] == "" {
			key = c.Type + " " + c.Metadata["name"]
		}
		out[key] = c
	}
	return out
}

func keys(m map[string]Chunk) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}

func TestAllChunkTypes(t *testing.T) {
	src, err := os.ReadFile("testdata/all.rs")
	if err != nil {
		t.Fatal(err)
	}
	got := kinds(t, string(src))
	for _, want := range []string{
		"use use std::io::{self, Read};", "struct Point", "struct Unit", "enum Shape", "union Bits", "constant MAX",
		"static COUNTER", "type Id", "macro twice", "trait Area", "constant Area::SIDES", "type Area::Unit",
		"method Area::area", "method Area::describe", "impl Point<T>", "method Point::new", "impl Area for Shape",
		"method Shape::area", "constant Shape::SIDES", "module geometry", "function geometry::dist", "module ext",
		"module tests", "function tests::it_works", "function ffi", "function main",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing chunk %q; have %v", want, keys(got))
		}
	}
	if c := got["struct Point"]; !strings.HasPrefix(c.Content, "/// A point.\n#[derive(Debug, Clone)]") || c.Metadata["attributes"] != "#[derive(Debug, Clone)]" ||
		c.Metadata["visibility"] != "pub" || c.Metadata["type_params"] != "<T>" || strings.Contains(c.Content, "Crate docs") {
		t.Errorf("struct chunk wrong: %+v", c)
	}
	if c := got["trait Area"]; strings.Contains(c.Content, "fn area") || c.Metadata["extends"] != "Send" {
		t.Errorf("trait header chunk wrong: %+v", c)
	}
	if c := got["method Area::area"]; c.Metadata["bodyless"] != "true" || c.Metadata["receiver"] != "Area" {
		t.Errorf("trait method wrong: %+v", c)
	}
	if c := got["method Point::new"]; c.Metadata["modifiers"] != "async" || c.Metadata["return_type"] != "Self" || c.Metadata["receiver"] != "Point" ||
		c.Metadata["signature"] != "pub async fn new(x: T, y: T) -> Self" || !strings.HasPrefix(c.Content, "/// Build one.") {
		t.Errorf("method chunk wrong: %+v", c)
	}
	if c := got["impl Area for Shape"]; c.Metadata["trait"] != "Area" || c.Metadata["type"] != "Shape" || strings.Contains(c.Content, "fn area") {
		t.Errorf("impl header chunk wrong: %+v", c)
	}
	if c := got["function geometry::dist"]; c.Metadata["namespace"] != "geometry" || c.Metadata["attributes"] != "#[inline]" {
		t.Errorf("module function wrong: %+v", c)
	}
	if c := got["function ffi"]; c.Metadata["modifiers"] != `unsafe extern "C"` {
		t.Errorf("fn modifiers wrong: %+v", c)
	}
}

func TestCommentsAndAttributes(t *testing.T) {
	src, err := os.ReadFile("testdata/lib.rs")
	if err != nil {
		t.Fatal(err)
	}
	got := kinds(t, string(src))
	if c := got["struct Cfg"]; !strings.HasPrefix(c.Content, "// A plain comment.\n#[derive(Debug)]") {
		t.Errorf("leading comment/attribute lost: %+v", c)
	}
	if c := got["function after"]; strings.Contains(c.Content, "trailing") {
		t.Errorf("trailing comment of the previous line leaked: %q", c.Content)
	}
}

func TestEmptyAndCommentOnly(t *testing.T) {
	if chunks, _ := parse("a.rs", nil); chunks == nil || len(chunks) != 0 {
		t.Fatalf("empty file must be [], got %#v", chunks)
	}
	got := kinds(t, "// just a comment\n")
	if _, ok := got["file src/x.rs"]; len(got) != 1 || !ok {
		t.Fatalf("comment-only file must yield one file chunk: %v", keys(got))
	}
}

func TestSyntaxErrorKeepsParsedDeclarations(t *testing.T) {
	got := kinds(t, "fn ok() {}\nstruct (\n")
	if _, ok := got["function ok"]; !ok {
		t.Fatalf("lost valid function: %v", keys(got))
	}
}

func TestLongFunctionIsSplit(t *testing.T) {
	body := strings.Repeat("    let x = 1;\n", 2000)
	chunks, err := parse("a.rs", []byte("fn big() {\n"+body+"}\n"))
	if err != nil || len(chunks) < 2 || chunks[0].Metadata["part"] == "" {
		t.Fatalf("expected split parts, got %d chunks, err %v", len(chunks), err)
	}
}

func TestOversizedFileBecomesPlainText(t *testing.T) {
	chunks, err := parse("big.rs", []byte(strings.Repeat("// padding padding padding\n", maxParseSize/20)))
	if err != nil || len(chunks) < 2 {
		t.Fatalf("want split file chunks, got %d, err %v", len(chunks), err)
	}
	for _, c := range chunks {
		if c.Type != "file" || c.Metadata["reason"] != "too_large" {
			t.Fatalf("unexpected chunk %s %v", c.Type, c.Metadata)
		}
	}
}

func TestHugeFileIsSkipped(t *testing.T) {
	chunks, err := parse("huge.rs", []byte(strings.Repeat("x", maxFallbackSize+1)))
	if err != nil || chunks == nil || len(chunks) != 0 {
		t.Fatalf("want [], got %d chunks, err %v", len(chunks), err)
	}
}

func TestSplitKeepsValidUTF8(t *testing.T) {
	body := strings.Repeat("let s = \"ёёёёёёёёёёёёёёёёёёёё\"; ", 600) // one long line: forces the no-newline fallback
	chunks, err := parse("a.rs", []byte("fn u() { "+body+"}\n"))
	if err != nil || len(chunks) < 2 {
		t.Fatalf("expected split, got %d, err %v", len(chunks), err)
	}
	for _, c := range chunks {
		if !utf8.ValidString(c.Content) {
			t.Fatalf("invalid utf-8 in part %s", c.Metadata["part"])
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("struct A; impl A { fn f(&self) {} }"))
	f.Fuzz(func(t *testing.T, b []byte) {
		// Syntax garbage is fine; a recovered panic is not.
		if _, err := parse("f.rs", b); err != nil && strings.Contains(err.Error(), "panic") {
			t.Fatal(err)
		}
	})
}

// Golden files pin content and metadata, not just chunk presence. Regenerate
// with: go test -tags "grammar_subset grammar_subset_rust" -run Golden -update
var update = flag.Bool("update", false, "rewrite golden files")

func TestGolden(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.rs")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		chunks, err := parse("src/"+filepath.Base(f), src)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.MarshalIndent(chunks, "", "  ")
		golden := strings.TrimSuffix(f, filepath.Ext(f)) + ".golden.json"
		if *update {
			if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update)", err)
		}
		if strings.TrimSpace(string(want)) != string(got) {
			t.Errorf("%s differs from %s (run with -update if intended)", f, golden)
		}
	}
}
