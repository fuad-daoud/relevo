#!/bin/sh
# scripts/board-assets.sh -- bundles the dev-only page under board/ into the
# committed assets under internal/board/assets, then writes the licence bundle
# and the integrity manifest. Dev-only: CI has neither node nor network, so the
# assets are committed and `make check` only reads them.
set -eu

# The paths sit next to this script's real repo path, not the caller's cwd.
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007
root=$(CDPATH= cd -- "$here/.." && pwd)
board="$root/board"
out="$root/internal/board/assets"

cd "$board"
npm ci --no-audit --no-fund

rm -rf "$out"
mkdir -p "$out" "$out/fonts"

node_modules/.bin/esbuild src/index.jsx \
	--bundle --minify --format=iife --jsx=automatic --platform=browser \
	--define:process.env.NODE_ENV='"production"' \
	--banner:js='window.EXCALIDRAW_ASSET_PATH="/assets/";' \
	--legal-comments=none --log-level=warning \
	--outfile="$out/bundle.js"

# Rewrite the compiled-in assets fallback. Excalidraw 0.18.1's prod bundle
# compiles its CDN base (https://esm.sh/.../dist/prod/) in as
# ASSETS_FALLBACK_URL and appends it to every font candidate list, so setting
# window.EXCALIDRAW_ASSET_PATH adds a local candidate but never removes the CDN
# one. Replace that base with the local /assets/ and fail hard unless the 0.18.1
# template is found exactly once and no https://esm.sh/ remains, so an
# Excalidraw bump cannot silently reintroduce the CDN.
BOARD_BUNDLE="$out/bundle.js" node --input-type=module <<'NODE'
import { readFileSync, writeFileSync } from "node:fs";

const file = process.env.BOARD_BUNDLE;
const template = 'https://esm.sh/${Cr.PKG_NAME?`${Cr.PKG_NAME}@${Cr.PKG_VERSION}`:"@excalidraw/excalidraw"}/dist/prod/';
const local = "/assets/";

const js = readFileSync(file, "utf8");
const found = js.split(template).length - 1;
if (found !== 1) {
  console.error(
    `board-assets: expected exactly one Excalidraw 0.18.1 assets-fallback template in ${file}, found ${found}`,
  );
  process.exit(1);
}

const rewritten = js.replace(template, local);
if (rewritten.includes("https://esm.sh/")) {
  console.error(`board-assets: a https://esm.sh/ reference remains in ${file}`);
  process.exit(1);
}
writeFileSync(file, rewritten);
NODE

cp index.html "$out/index.html"
cp node_modules/@excalidraw/excalidraw/dist/prod/index.css "$out/index.css"
cp -R node_modules/@excalidraw/excalidraw/dist/prod/fonts/. "$out/fonts/"

# LICENSES.txt names every shipped package and carries the two licence texts
# the assets fall under: MIT for the JavaScript, SIL OFL 1.1 for the fonts.
cat > "$out/LICENSES.txt" <<'EOF'
relevo board -- third-party licences

Bundled JavaScript (MIT):
  react 18.3.1                         https://github.com/facebook/react
  react-dom 18.3.1                     https://github.com/facebook/react
  @excalidraw/excalidraw 0.18.1        https://github.com/excalidraw/excalidraw
  @excalidraw/mermaid-to-excalidraw 1.1.2
                                       https://github.com/excalidraw/mermaid-to-excalidraw

Self-hosted fonts (SIL Open Font License 1.1):
  Cascadia Code                        https://github.com/microsoft/cascadia-code
  Assistant                            https://github.com/IndianTypeFoundry/assistant-font
  Excalifont                           https://github.com/excalidraw/excalifont

--------------------------------------------------------------------------------
MIT License (the bundled JavaScript)

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

--------------------------------------------------------------------------------
SIL Open Font License 1.1 (the self-hosted fonts)

This Font Software is licensed under the SIL Open Font License, Version 1.1.
This license is copied below, and is also available with a FAQ at:
https://openfontlicense.org

SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide development
of collaborative font projects, to support the font creation efforts of
academic and linguistic communities, and to provide a free and open framework in
which fonts may be shared and improved in partnership with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The fonts,
including any derivative works, can be bundled, embedded, redistributed and/or
sold with any software provided that any reserved names are not used by
derivative works. The fonts and derivatives, however, cannot be released under
any other type of license. The requirement for fonts to remain under this
license does not apply to any document created using the fonts or their
derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright Holder(s)
under this license and clearly marked as such. This may include source files,
build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the copyright
statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting, or
substituting -- in part or in whole -- any of the components of the Original
Version, by changing formats or by porting the Font Software to a new
environment.

"Author" refers to any designer, engineer, programmer, technical writer or
other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining a copy of
the Font Software, to use, study, copy, merge, embed, modify, redistribute, and
sell modified and unmodified copies of the Font Software, subject to the
following conditions:

1) Neither the Font Software nor any of its individual components, in Original
or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy contains
the above copyright notice and this license. These can be included either as
stand-alone text files, human-readable headers or in the appropriate
machine-readable metadata fields within text or binary files as long as those
fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font Name(s)
unless explicit written permission is granted by the corresponding Copyright
Holder. This restriction only applies to the primary font name as presented to
the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font Software
shall not be used to promote, endorse or advertise any Modified Version, except
to acknowledge the contribution(s) of the Copyright Holder(s) and the Author(s)
or with their explicit written permission.

5) The Font Software, modified or unmodified, in part or in whole, must be
distributed entirely under this license, and must not be distributed under any
other license. The requirement for fonts to remain under this license does not
apply to any document created using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT OF COPYRIGHT, PATENT, TRADEMARK, OR
OTHER RIGHT. IN NO EVENT SHALL THE COPYRIGHT HOLDER BE LIABLE FOR ANY CLAIM,
DAMAGES OR OTHER LIABILITY, INCLUDING ANY GENERAL, SPECIAL, INDIRECT, INCIDENTAL,
OR CONSEQUENTIAL DAMAGES, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE,
ARISING FROM, OUT OF THE USE OR INABILITY TO USE THE FONT SOFTWARE OR FROM OTHER
DEALINGS IN THE FONT SOFTWARE.
EOF

# assets.sha256 is the integrity list: one "<sha256>  <path>" line per file
# beside it, paths relative to this directory and sorted, so a second run of
# this script is byte-identical.
if command -v sha256sum >/dev/null 2>&1; then
	sum() { sha256sum "$1" | cut -d' ' -f1; }
else
	sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

cd "$out"
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

find . -type f ! -name assets.sha256 | sed 's|^\./||' | LC_ALL=C sort > "$work/list"
: > assets.sha256
while IFS= read -r f; do
	printf '%s  %s\n' "$(sum "$f")" "$f" >> assets.sha256
done < "$work/list"

echo "board-assets: wrote $(find "$out" -type f | wc -l | tr -d ' ') files to $out"
