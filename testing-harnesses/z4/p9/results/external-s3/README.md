# External S3 client on a native store

A signed AWS SDK client checked HEAD, whole and ranged GET, conditional GET and PUT, native-source COPY, and independently verified a multipart upload on a store with exact native Coreutils bodies. The C3 binary and SDK harness executable hashes are recorded. An index published before SDK writes was diagnosed as stale afterward. GC, repack and index refresh then completed, and deep verify reported zero missing, corrupt or invalid data, including two authority bodies.

The existing managed profile independently passed 264 cases on the same binary, including 133 AWS SDK cases, 129 minio-go cases and two restart cases. It reported two skips. The MinIO client omitted an unsupported CRC32 check and the AWS CLI was unavailable. This validates both SDK clients against ZeroS3. Server baselines against external implementations remain open.

The same existing profile was run against checksum-verified MinIO RELEASE.2025-04-22T22-12-26Z and VersityGW v1.11.0 on isolated loopback servers. MinIO passed 224 cases with four failures on duplicate-slash and NUL-containing object keys. VersityGW passed 227 and failed nine, including data at directory keys, NUL-containing keys, and AWS SDK copy assertions. Full original per-client failures are preserved.
