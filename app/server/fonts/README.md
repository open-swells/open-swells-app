Inter 4.1 (https://github.com/rsms/inter/releases/tag/v4.1), the site's
typeface, for the server-drawn share cards. SIL Open Font License; see
LICENSE.txt.

Each file is the release's `extras/ttf/Inter-<Weight>.ttf` cut down to
Latin, general punctuation and arrows:

    pyftsubset Inter-<Weight>.ttf \
      --unicodes="U+0020-007E,U+00A0-017F,U+2010-2027,U+2032-2033,U+2190-2193" \
      --layout-features='kern' --no-hinting --output-file=Inter-<Weight>.ttf

Characters outside that range draw as the font's missing glyph.
