# Encrypted Usenet archives

Ether streams password-protected **stored (uncompressed) RAR4, RAR5 and 7z**
members, including archives with encrypted filenames/headers. RAR encrypted
headers and multi-volume ciphertext are inspected before serving media. 7z
supports AES-only or AES followed by Copy for media; encoded headers may also
use bounded LZMA/LZMA2 decompression. Compressed or solid media and legacy
pre-AES RAR encryption remain unsupported.

Passwords come from the NZB's existing `password`, `pass` or `pwd` metadata.
An indexer's password flag no longer rejects Usenet results before fetching
and validating the NZB. Passworded torrent/debrid results remain rejected.
A missing password fails closed. Header CRCs and available RAR5 password checks
are verified; the selected decrypted media also needs a recognized container
signature before its plan can pass health validation. An incorrect password
can be indistinguishable from damaged content in formats without a password
check. All ordinary article sampling and media probing still apply.

## Reading and caching

The segment caches retain yEnc-decoded **archive ciphertext**. A release plan
holds derived keys in memory; no decrypted media or derived keys are written
by the archive layer. The existing NZB payload digest includes its password
metadata, so corrected metadata produces a different plan. Existing NZB
persistence policies are unchanged.

A seek aligns the requested plaintext range to AES blocks, fetches its
preceding ciphertext block (or uses the archive IV at offset zero), and decrypts
only those blocks. The reader handles an AES block split across archive volumes
and files that start partway through a 7z folder. Scratch space is bounded to
64 KiB per CBC reader. Large demand reads use bounded workers to preserve NNTP
article concurrency; prefetch remains at background priority. Durable cache
warming includes predecessor blocks and padding.

Password derivation has work-factor, work and memory limits and honors request
cancellation. RAR volume parsers share a bounded cache and coalesce identical
key derivations; different derivations are serialized per release. Parse work
budgets do not accumulate permanently across all volumes or canceled attempts.
A caller waiting for another request's archive plan can cancel independently.

CBC is not authenticated encryption. Header checks, yEnc CRCs and a media
signature do **not** prove a whole-file archive checksum. Random seeks do not
read an entire movie solely to calculate its checksum. Missing/corrupt articles
continue through backfill or source failover; the damage concealment path never
invents ciphertext or pads decrypted archive media.

## Verification

Offline regression tests cover:

- Real upstream RAR encrypted-header and payload fixtures, with retained ISC
  provenance; Ether-generated RAR4/RAR5 Unicode and multi-volume fixtures
  independently verified by 7-Zip.
- 7-Zip-generated encrypted content, encrypted/compressed headers, Unicode
  passwords, and compressed-media rejection.
- Whole-output equality, random/unaligned/concurrent seeks, split AES blocks,
  multiple 7z files and folder offsets, missing/wrong passwords, malformed
  metadata, cancellation, key-derivation limits and coalescing.
- Engine health/Open, password-plan isolation, cache-backed ranges with the
  provider offline, background priority, bounded parallel demand reads, and
  absence of synthetic recovery bytes for encrypted data.
- Real loopback NNTP sockets through the native yEnc decoder and CBC reader,
  with latency, truncated bodies, corrupt yEnc, mid-body stalls and primary
  article misses; backfill returns byte-identical plaintext.
- Password-enabled RAR/7z fuzz targets with bounded inputs and cancellation.

Fixture payloads are synthetic media-like bytes, not copyrighted movies or a
claim of physical Plex-client playback certification. No live provider or
credentials are needed. See `THIRD_PARTY_NOTICES.md` for the original Go
implementations and retained BSD notices.
