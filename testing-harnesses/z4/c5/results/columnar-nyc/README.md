# NYC TLC columnar append histories

The January and February 2024 public yellow taxi Parquet files are identified by hash. The generator takes 80,000 January rows and appends two 20,000-row batches from February. These are derived chronological revisions, produced with PyArrow 26.0.1 and DuckDB 1.4.4. Each variant reopened as exactly the same logical table.

All six matched store comparisons passed whole-file SHA-256, twelve cache-purged and twelve repeated ranges per mode, and deep verify after reclamation. B2 delta used 9–32% less allocated disk than CDC. The structural form used 4.5–6.8 times CDC's allocated bytes. Whole throughput and range timings are one shared-host trial per mode. The structural mode did not meet the adoption threshold.

[Public NYC TLC source](https://www.nyc.gov/site/tlc/about/tlc-trip-record-data.page)
