# Third-party notices

Ether includes or depends on the following third-party software and data.

## github.com/ulikunitz/xz

- Used by: `internal/archive` (LZMA/LZMA2 decoding of encoded 7z headers)
- Licence: BSD-3-Clause
- URL: https://github.com/ulikunitz/xz
- Copyright (c) 2014-2022 Ulrich Kunitz. All rights reserved.

## AltMount test fixtures

- Used by: `internal/archive/testdata/altmount/` (test data only, not shipped):
  `archive.rar`, `archive.part01.rar`, `archive.part02.rar`, `compressed.rar`,
  `encrypted.rar`, `solid.rar`, `multi.part1.rar`, `multi.part2.rar`,
  `multi.part3.rar`, `payload_a.bin`, `payload_b.bin`
- Source: AltMount `internal/importer/testdata/`
- Licence: MIT
- URL: https://github.com/kipsilabs/altmount
- Copyright (c) 2025 Javier Blanco

MIT License text:

> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in all
> copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.
