# Vendored Preact — manifest (RF-23)

**Preact 10.29.2**, MIT (see `LICENSE`). Vendored npm-free (D-32) — no registry fetch at
build time. Upstream: `https://unpkg.com/preact@10.29.2/`.

| Bare specifier | Vendored file | Upstream path |
| --- | --- | --- |
| `preact` | `preact.module.js` | `dist/preact.module.js` |
| `preact/hooks` | `hooks.module.js` | `hooks/dist/hooks.module.js` |
| `preact/jsx-runtime` | `jsx-runtime.module.js` | `jsx-runtime/dist/jsxRuntime.module.js` |

**Alias coverage.** The vendored files import only the bare specifier `preact` (verified),
which the `Alias` map in `cmd/build/main.go` resolves to the absolute file paths. Adding a
Preact sub-path requires adding **both** the vendored file **and** its alias entry —
otherwise the build fails loudly (esbuild cannot resolve it; there is no silent
`node_modules` fallback). This is the SPIKE-1 build contract.

**To update.** Re-fetch the three `*.module.js` files + `LICENSE` from unpkg at the new
version, update this manifest's version, and re-run the SPIKE-1 browser smoke (the island
must mount and the counter must increment).
