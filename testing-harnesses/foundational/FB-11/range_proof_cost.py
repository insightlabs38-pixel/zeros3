"""Price one aligned 4 KiB range in a manifest-bound balanced Merkle tree."""

import json

DIGEST_BYTES = 32
RANGE_BYTES = 4 << 10


def price(object_bytes, block_bytes):
    leaves = (object_bytes + block_bytes - 1) // block_bytes
    depth = (leaves - 1).bit_length()
    padded_leaves = 1 << depth
    sibling_bytes = depth * DIGEST_BYTES
    return {
        "object_bytes": object_bytes,
        "block_bytes": block_bytes,
        "sibling_bytes": sibling_bytes,
        "proof_and_covering_block_bytes": sibling_bytes + block_bytes,
        "returned_range_bytes": RANGE_BYTES,
        "tree_bytes": (2 * padded_leaves - 1) * DIGEST_BYTES,
        "initial_content_bytes_hashed": object_bytes,
    }


if __name__ == "__main__":
    for object_bytes in [64 << 20, 2 << 30, 1 << 40]:
        for block_bytes in [4 << 10, 16 << 10, 64 << 10]:
            print(json.dumps(price(object_bytes, block_bytes), sort_keys=True))
