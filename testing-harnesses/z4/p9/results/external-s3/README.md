# External S3 client on a native store

A signed AWS SDK client checked HEAD, whole and ranged GET, conditional GET and PUT, native-source COPY, and independently verified a multipart upload on a store with exact native Coreutils bodies. The C3 binary and SDK harness executable hashes are recorded. An index published before SDK writes was diagnosed as stale afterward. GC, repack and index refresh then completed, and deep verify reported zero missing, corrupt or invalid data, including two authority bodies.

The existing managed profile independently passed 264 cases on the same binary, including 133 AWS SDK cases, 129 minio-go cases and two restart cases. It reported two skips. The MinIO client omitted an unsupported CRC32 check and the AWS CLI was unavailable. This validates both SDK clients against ZeroS3. Server baselines against external implementations remain open.
