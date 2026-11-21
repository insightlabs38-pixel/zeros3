# EROFS LZ4HC release images

The experiment encoded the published Coreutils 9.6 and 9.7 tar files with mkfs.erofs 1.8.6 at LZ4HC levels 9 and 12 in single 4 KiB physical clusters. The upstream tool extracted the exact original archives. The Go compact-index reader agreed with dump.erofs on the eligible counts and 64 sampled spans per image. Another mkfs.erofs release, 1.9.4, independently produced and extracted two images from the 9.6 archive, and every sampled cluster replayed exactly.

The exact full recipe rebuilt all four 1.8.6 images from the actual archive bytes and recorded literal layout. On a maintained ZeroS3 store with ordinary uncompressed packing of those objects the basis and recipes occupied 120,414,208 bytes, versus 59,482,112 for B2 delta. Repacking all three stores with comparable compression reduced the recipe and basis to 30,326,784 allocated bytes, versus 54,390,784 for B2 and 79,994,880 for CDC. The recipe took 99.4 s to rebuild four whole images under shared load, versus 82.9 s for B2 and 2.7 s for CDC.

The separate indexed-range screen authenticates the exact intersecting stored clusters, charging the serialized digest index separately. These results characterize a bounded experimental format. The pricing still needs integrated range and whole throughput measurements on broader workloads before production admission.

The indexed probe verified 128 exact 4 KiB ranges on the four images. The median sampled replay took 1.19 ms under shared load and read at most 35,574 original basis bytes per request. Each serialized index added 295,722–298,495 bytes to the raw recipe and basis. These timing probes used the in-memory index built from verified image bytes and excluded index loading.
