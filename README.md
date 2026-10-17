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

## Use

```yaml
indexer:
  include: ["**/*.rs"]
  parsers:
    - extensions: [".rs"]
      type: "wasm"
      path: ".onfly/plugins/rust-parser.wasm"
```

Third-party licenses: see THIRD_PARTY_NOTICES.
