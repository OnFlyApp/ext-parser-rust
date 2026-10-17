package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// TestWasm runs the built module under wazero, the runtime the Onfly CLI uses.
// Enabled by RS_WASM=bin/rust-parser.wasm (CI sets it after `make wasm`).
func TestWasm(t *testing.T) {
	path := os.Getenv("RS_WASM")
	if path == "" {
		t.Skip("RS_WASM not set")
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(wasm) > 25<<20 {
		t.Fatalf("wasm is %d MiB, budget is 25 MiB", len(wasm)>>20)
	}
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)
	cm, err := r.CompileModule(ctx, wasm)
	if err != nil {
		t.Fatal(err)
	}
	run := func(src []byte) ([]Chunk, time.Duration) {
		var out bytes.Buffer
		cfg := wazero.NewModuleConfig().WithArgs("plugin", "src/x.rs").
			WithStdin(bytes.NewReader(src)).WithStdout(&out).WithStderr(os.Stderr).WithName("")
		start := time.Now()
		if _, err := r.InstantiateModule(ctx, cm, cfg); err != nil {
			t.Fatal(err)
		}
		d := time.Since(start)
		var chunks []Chunk
		if err := json.Unmarshal(out.Bytes(), &chunks); err != nil {
			t.Fatal(err)
		}
		return chunks, d
	}

	src, _ := os.ReadFile("testdata/all.rs")
	chunks, _ := run(src)
	types := map[string]bool{}
	for _, c := range chunks {
		types[c.Type] = true
	}
	for _, want := range []string{"use", "struct", "enum", "trait", "impl", "method", "function", "module", "constant", "static", "type", "macro"} {
		if !types[want] {
			t.Errorf("wasm output has no %q chunk", want)
		}
	}

	// The host cancels a parse at 5 s. The largest file we still parse must come
	// back well before that.
	var b strings.Builder
	for i := 0; b.Len() < maxParseSize-1024; i++ {
		fmt.Fprintf(&b, "pub struct C%d { x: i32 }\n\nimpl C%d {\n    pub fn a(&self, x: i32) -> i32 { x + %d }\n\n    pub fn b(&self) {\n        for v in [1, 2, 3] {\n            println!(\"{}\", v);\n        }\n    }\n}\n\n", i, i, i)
	}
	chunks, d := run([]byte(b.String()))
	t.Logf("%d KiB -> %d chunks in %s", b.Len()>>10, len(chunks), d.Round(time.Millisecond))
	if len(chunks) == 0 {
		t.Fatal("largest accepted file produced no chunks")
	}
	if d > 4500*time.Millisecond {
		t.Fatalf("took %s; host limit is 5s, lower maxParseSize", d)
	}
}
