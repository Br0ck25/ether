#!/usr/bin/env bash
# gen7z.sh regenerates the 7z fixtures in testdata/7z. The archives are
# committed; tests never run this script. Requires 7zz (7-Zip 23+) and python3.
#
#   bash internal/archive/testdata/gen7z.sh
#
# Payloads are fixed-seed pseudo-random bytes (so offset bugs cannot hide
# behind a periodic pattern) and are committed next to the archives.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p 7z
cd 7z
rm -rf work ./*.7z ./*.7z.0*
mkdir -p work/sub work/emptydir

python3 -I - <<'PY'
import random
def w(path, seed, n):
    open(path, "wb").write(random.Random(seed).randbytes(n))
w("rand_a.bin", 1, 60000)
w("rand_b.bin", 2, 33333)
w("rand_c.bin", 3, 5000)
PY
cp rand_a.bin rand_b.bin work/
cp rand_c.bin work/sub/
: > work/empty.txt

(
  cd work
  # single stored file
  7zz a -bso0 -mx0 ../store_single.7z rand_a.bin
  # several stored files, a subdirectory, an empty file and an empty directory
  7zz a -bso0 -mx0 ../store_multi.7z rand_a.bin rand_b.bin sub empty.txt emptydir
  # stored with an uncompressed (plain kHeader) header
  7zz a -bso0 -mx0 -mhc=off ../store_plainhdr.7z rand_b.bin sub
  # multi-volume stored set (.7z.001, .002, ...)
  7zz a -bso0 -mx0 -v40k ../store_vol.7z rand_a.bin rand_b.bin
  # compressed (LZMA2)
  7zz a -bso0 -mx5 ../compressed.7z rand_b.bin sub
  # compressed with classic LZMA plus a BCJ filter
  7zz a -bso0 -m0=BCJ -m1=LZMA ../bcj_lzma.7z sub/rand_c.bin
  # encrypted content, plain names
  7zz a -bso0 -mx0 -pSecret ../enc_content.7z rand_b.bin
  # encrypted content and header
  7zz a -bso0 -mx0 -pSecret -mhe=on ../enc_header.7z rand_b.bin
)

# many small stored files: forces an LZMA-encoded header (kEncodedHeader)
mkdir -p work/many
for i in $(seq -w 0 199); do
  printf 'small file %s\n' "$i" > "work/many/f$i.txt"
done
(cd work/many && 7zz a -bso0 -mx0 ../../store_many.7z .)

rm -rf work
ls -l
