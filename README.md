# plugin-vm

The `plugin-vm` plugin candy of the [opencharly/charly](https://github.com/opencharly/charly)
candy library, as a standalone repo (the candy de-submodule cutover, plugin
kind). The Go module lives at `candy/plugin-vm/` with module path
`github.com/opencharly/plugin-vm/candy/plugin-vm`; the charly resolver fetches this repo at the pinned tag and
the compiled-in wiring imports the module at that path.

## Platforms

Builds for `linux/{amd64, arm64}` and, since the `sdk` 32-bit fix
([opencharly/sdk#263](https://github.com/opencharly/sdk/pull/263), issue
[#262](https://github.com/opencharly/sdk/issues/262)), `linux/arm/v7`:

```sh
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build ./cmd/serve
```

`CGO_ENABLED=0` produces a static binary, which is what an armv7 appliance
without a glibc toolchain (e.g. a JetKVM's uClibc userland) needs.
