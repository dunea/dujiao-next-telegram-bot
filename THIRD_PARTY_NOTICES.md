# Third-party notices

This software includes Go and third-party Go modules. The application's MIT license does not replace their respective licenses. Release bundles include original notices under `third-party/`; `build-info.txt` identifies the exact module versions linked into the executable.

The bot uses Go's runtime and standard library, Telegram and Redis clients, QR-code encoding and rendering, and related hashing, image and text modules. The packaging script collects license and notice files from the main executable's dependency graph and stops if a dependency has no license file.

## FreeType-Go

This software is based in part on the work of the FreeType Team, including FreeType-Go. See https://www.freetype.org/ and https://github.com/golang/freetype. We use FreeType-Go under the FreeType License (FTL), not the alternative GPL option. The unchanged upstream `LICENSE` and `licenses/ftl.txt` are included in `third-party/github.com/golang/freetype/` in binary distributions; the original upstream licenses directory is preserved in full.

## Reed-Solomon encoding

`github.com/yeqown/reedsolomon` is pinned to upstream commit `c5f4bc9af094852b52e593a5f964647c43028c51`, which adds the MIT license without changing the implementation relative to `v1.0.0`. See the [upstream comparison](https://github.com/yeqown/reedsolomon/compare/v1.0.0...c5f4bc9af094852b52e593a5f964647c43028c51). The original license is included in the release bundle.
