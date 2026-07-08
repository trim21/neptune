"""Generate deterministic torrent test fixtures for Neptune integration tests.

Usage:
    uv run python e2e/gen_fixtures.py

Outputs to testdata/fixtures/:
    - test-fixture.torrent
    - test-fixture/           (data directory)

Covers these edge cases:
    1. Piece boundary crossing file boundary (file_a + file_b share a piece)
    2. Subdirectories (sub/nested.bin)
    3. File smaller than piece length (tiny.bin)
    4. File exactly aligned to piece boundary (aligned.bin)
    5. Empty file (empty.txt, 0 bytes)
    6. File spanning multiple pieces (large.bin)
    7. Non-ASCII filename (日本語.txt)

Piece length is 32 KiB. All file content is deterministic (seeded PRNG).
"""

import hashlib
import os

import bencode2

PIECE_LENGTH = 4 * 1024 * 1024  # 4 MiB

FILES = [
    (["file_a.bin"], 3 * 1024 * 1024),  # 3 MiB, shares piece with file_b
    (["file_b.bin"], 20 * 1024 * 1024),  # 20 MiB, crosses multiple pieces
    (["sub", "nested.bin"], 10 * 1024 * 1024),  # 10 MiB, subdirectory
    (["tiny.bin"], 100),  # smaller than piece
    (["aligned.bin"], PIECE_LENGTH),  # exactly 1 piece
    (["empty.txt"], 0),  # 0 bytes
    (["large.bin"], 63 * 1024 * 1024 + 12_345),  # ~63 MiB
    (["日本語.txt"], 4_096),  # non-ASCII filename
]


def generate_data(offset: int, size: int) -> bytes:
    return bytes((offset + i) % 17 for i in range(size))


def build_pieces(data: bytes) -> bytes:
    pieces = b""
    for offset in range(0, len(data), PIECE_LENGTH):
        chunk = data[offset : offset + PIECE_LENGTH]
        pieces += hashlib.sha1(chunk).digest()
    return pieces


def main():
    name = "test-fixture"
    out_dir = os.path.join("testdata", "fixtures")
    data_dir = os.path.join(out_dir, name)

    data_offset = 0
    all_data = b""
    for path_parts, size in FILES:
        full_path = os.path.join(data_dir, *path_parts)
        os.makedirs(os.path.dirname(full_path), exist_ok=True)
        file_data = generate_data(data_offset, size)
        data_offset += size
        with open(full_path, "wb") as f:
            f.write(file_data)
        all_data += file_data

    pieces = build_pieces(all_data)

    file_infos = []
    for path_parts, size in FILES:
        file_infos.append(
            {b"path": [p.encode("utf-8") for p in path_parts], b"length": size}
        )

    info = {
        b"piece length": PIECE_LENGTH,
        b"pieces": pieces,
        b"name": name.encode("utf-8"),
        b"files": file_infos,
    }

    info_bytes = bencode2.bencode(info)

    metainfo = {
        b"announce": b"http://127.0.0.1:9999/announce",
        b"info": info_bytes,
    }

    torrent_path = os.path.join(out_dir, name + ".torrent")
    with open(torrent_path, "wb") as f:
        f.write(bencode2.bencode(metainfo))

    info_hash = hashlib.sha1(info_bytes).hexdigest()
    total_size = sum(size for _, size in FILES)
    num_pieces = (total_size + PIECE_LENGTH - 1) // PIECE_LENGTH

    print("Generated test fixture:")
    print(f"  Torrent:   {torrent_path}")
    print(f"  Data:      {data_dir}/")
    print(f"  InfoHash:  {info_hash}")
    print(f"  Piece len: {PIECE_LENGTH // 1024} KiB")
    print(f"  Pieces:    {num_pieces}")
    print(f"  Files:     {len(FILES)}")
    print(f"  Total:     {total_size} bytes")


if __name__ == "__main__":
    main()
