# Direct Parquet page ranges

The normal read path now uses the existing page decoder for bounded ranges of existing Parquet representations. The focused suite tested actual hard-codec fixtures after reclaiming their CDC copies.

The separate page-cap screen used the first two C1 generated table revisions. CDC used 27,698,335 file bytes. Full structured replay used 42,344,520, with a 703 ms median on the six sampled 4 KiB reads. The experimental 1 MiB expanded-page cap used 33,291,386 bytes and 4.13 ms median. CDC's median was 0.50 ms and the capped whole throughput was 5.20 MiB/s against CDC's 49.80. The cap failed both storage and whole-read screening. The production code contains the direct range path. The experimental cap and test are preserved in the compressed patch.
