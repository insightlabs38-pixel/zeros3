# Isolated archive redundancy trial

A parity sidecar over eight 1 MiB windows of the published GNU Coreutils 9.7 archive used 1,048,906 bytes for 8,388,608 protected bytes. Each omitted or damaged member was restored exactly against independent member digests. The race run also exercised damaged parity and mixed multiple-loss refusal. The prototype has no production format or integration with store readers.
