# ext-parser-rust

Rust parser plugin for **Onfly**. Built on [gotreesitter](https://github.com/odvcencio/gotreesitter) (pure-Go tree-sitter, no CGO) with the tree-sitter-rust grammar. Compiles to a WASI module for Onfly's wazero sandbox, or a native binary for stdio use.

```bash
onfly ext install rust
```

Handles `.rs`.

## Chunks

Input is one file on stdin, argv[1] is its path, output is the standard `[]Chunk` JSON.

| `type` | Source node | Notes |
|---|---|---|
| `use` | `use_declaration` | `name` = the declaration |
| `module` | `mod x;`, `mod x { ... }` | inline modules are header only; their items are chunked with `namespace` set |
| `struct` `enum` `union` `type` `constant` `static` `macro` | `*_item`, `macro_rules!` | whole declaration |
| `trait` | `trait_item` | header only (docs, attributes, bounds); members are separate chunks, `receiver` = the trait |
| `impl` | `impl_item` | header only (`impl<T> Tr for S<T>`); members are separate chunks, `receiver` = the implemented type |
| `function` `method` | `fn` | `method` inside `impl` and `trait`; trait methods without a body have `bodyless=true` |
| `statement` | other top-level code | consecutive statements merged (`extern` blocks, macro calls) |
| `file` | whole file | fallback when nothing else matched |

Metadata: `name`, `fqn` (`module::Type::item`), `namespace`, `receiver`, `start_line`, `end_line`, and where they apply `signature`, `params`, `return_type`, `type_params`, `type`, `visibility`, `modifiers` (`async`, `unsafe`, `const`, `extern "C"`), `attributes`, `extends` (trait bounds), `trait`, `bodyless`, `part`.

Doc comments (`///`, `/** */`), plain comments and `#[attributes]` directly above an item are part of the chunk; `//!` inner docs are not. Bodies over 8 KB are split at line boundaries (`part=1/3`). Files over 80 KiB are not parsed: they come back as plain `file` chunks (`reason=too_large`), and files over 2 MiB yield `[]`. The host cancels a parse at 5 s (and then keeps the stale chunks). Syntax errors do not fail the file: what parsed is chunked and a warning goes to stderr.

## Known limitations

- The wasm is about 18 MB (tree-sitter runtime plus the Rust grammar). Expect a few seconds to compile it once per host start.
- Items inside function bodies are part of the function chunk. Items produced by macros are not visible.
- Methods of `impl` blocks are keyed by the bare type name, so two `impl` blocks for the same type in one module share a `receiver`.

## Build

```bash
make wasm     # bin/rust-parser.wasm (reproducible, Go 1.27+)
make native   # bin/rust-parser
make test
```

## Test

```bash
make test                                                    # unit and golden tests
go test -tags "grammar_subset grammar_subset_rust" -run Golden -update    # regenerate testdata/*.golden.json after an intended change
make wasm && RS_WASM=bin/rust-parser.wasm go test -tags "grammar_subset grammar_subset_rust" -run TestWasm -v .   # built module under wazero
```

The golden files in `testdata/` pin content and metadata of every chunk. The wazero test checks the chunk types, the 25 MiB size budget, and that the largest file we still parse stays under the host's 5 s limit. CI also runs a short fuzz pass and builds the wasm twice to check the bytes match.

The chunk contract is described in the [`cli` docs](https://github.com/OnFlyApp/cli/blob/main/docs/parsers.md). Plugins are listed in the [registry](https://github.com/OnFlyApp/registry).

## Use

```yaml
indexer:
  include: ["**/*.rs"]
  parsers:
    - extensions: [".rs"]
      type: "wasm"
      path: ".onfly/plugins/rust-parser.wasm"
```

## Author

[Oleksii Ivanov](https://github.com/axitdev)

## License

Licensed under the **Functional Source License, Version 1.1, ALv2 Future License** (SPDX: `FSL-1.1-ALv2`), see [LICENSE](LICENSE).

Free for any purpose other than a competing use: making the software available in a commercial product or service that substitutes for it. Each version converts to Apache 2.0 two years after it is released.

Third-party licenses of the components embedded in the wasm: see THIRD_PARTY_NOTICES.
