#!/usr/bin/env python3
"""Extract embedded protobuf FileDescriptorProtos from the Antigravity CLI
binary (agy) and assemble a minimal FileDescriptorSet that can decode a
gemini_coder.Step payload (the `steps.step_payload` blob in
~/.gemini/antigravity-cli/conversations/<uuid>.db).

Why this exists: agy ships no .proto files on disk. protoc-gen-go embeds
each generated file's FileDescriptorProto as a raw serialized byte blob
inside the compiled binary; there's no length-prefixed container grouping
them, so each one has to be located and its length inferred from its own
wire-format content. Locating candidates: every FileDescriptorProto starts
with its `name` field (field 1, wire type 2 = LEN) serialized first, and
every name ends in ".proto" - so scan for
`<0x0a><varint length><...>.proto` and validate the captured string looks
like a plausible file path. Inferring each blob's length: walk the
generic protobuf wire format field-by-field from the candidate start
(tag/wire-type only, no message-specific schema needed) until a tag byte
that isn't a valid field-1 (varint/64bit/LEN/32bit) is hit - which is what
naturally happens at the true end of one embedded blob, where either
alignment padding (0x00, an invalid field number) or unrelated binary
content follows.

This is a real reverse-engineering technique with real limits: some
candidates fail to parse cleanly (~12% here) and a handful of names occur
twice with different sizes (kept: the larger, and in every case checked
here it was a strict superset of the smaller's message/enum types, not a
different schema version). Used only to decode the current user's own
local Antigravity conversation data - not to reproduce or redistribute
Google's source.
"""
import pickle
import re
import sys
from pathlib import Path

from google.protobuf import descriptor_pb2

DEFAULT_BINARY = Path.home() / ".local/bin/agy"
PATH_RE = re.compile(rb"^[A-Za-z0-9_/.\-]+\.proto$")
MAX_NAME_LEN = 300
MAX_FIELD_WALK = 400_000

# The only dependency the Step closure pulls in that agy's binary does not
# embed a descriptor for (see closure() below). It supplies exactly the
# three enums referenced from cortex.proto's browser-tool config fields.
STUB_BROWSER_PROTO_DEPS = {
    "third_party/jetski/browser_pb/browser.proto": {
        "package": "exa.browser_pb",
        "enums": {
            "ClickType": ["CLICK_TYPE_UNSPECIFIED"],
            "ScrollDirection": ["SCROLL_DIRECTION_UNSPECIFIED"],
            "WindowState": ["WINDOW_STATE_UNSPECIFIED"],
        },
    }
}


def read_varint(data, pos):
    result = 0
    shift = 0
    n = len(data)
    while True:
        if pos >= n:
            return None, None
        b = data[pos]
        result |= (b & 0x7F) << shift
        pos += 1
        if not (b & 0x80):
            return result, pos
        shift += 7
        if shift > 35:
            return None, None


def find_name_field_candidates(data):
    """Positions of `<0x0a><varint L><L bytes ending in '.proto'>`."""
    starts = []
    pos = 0
    while True:
        idx = data.find(b"\x0a", pos)
        if idx == -1:
            break
        pos = idx + 1
        length, after = read_varint(data, idx + 1)
        if length is None or length < 5 or length > MAX_NAME_LEN:
            continue
        candidate = data[after : after + length]
        if candidate.endswith(b".proto") and PATH_RE.match(candidate):
            starts.append(idx)
    return starts


def walk_generic_fields(data, pos, limit):
    """Furthest offset reachable via a run of syntactically valid
    protobuf fields starting at pos, capped at limit."""
    p = pos
    last_good = pos
    n = len(data)
    while p < limit:
        tag, p2 = read_varint(data, p)
        if tag is None:
            break
        field_no = tag >> 3
        wire_type = tag & 0x7
        if field_no == 0:
            break
        if wire_type == 0:
            _, p3 = read_varint(data, p2)
            if p3 is None:
                break
            p = p3
        elif wire_type == 1:
            p = p2 + 8
        elif wire_type == 2:
            length, p3 = read_varint(data, p2)
            if length is None:
                break
            p = p3 + length
        elif wire_type == 5:
            p = p2 + 4
        else:
            break
        if p > limit or p > n:
            break
        last_good = p
    return last_good


def extract_all(binary_path):
    data = binary_path.read_bytes()
    tag_starts = find_name_field_candidates(data)

    by_name = {}
    fail = 0
    for i, ts in enumerate(tag_starts):
        next_ts = tag_starts[i + 1] if i + 1 < len(tag_starts) else len(data)
        limit = min(next_ts, ts + MAX_FIELD_WALK, len(data))
        end = walk_generic_fields(data, ts, limit)
        blob = data[ts:end]
        fdp = descriptor_pb2.FileDescriptorProto()
        try:
            fdp.MergeFromString(blob)
        except Exception:
            fail += 1
            continue
        if not fdp.name.endswith(".proto"):
            fail += 1
            continue
        existing = by_name.get(fdp.name)
        if existing is None or len(blob) > len(existing[1]):
            by_name[fdp.name] = (fdp, blob)

    return by_name, len(tag_starts), fail


def build_stub(name, spec):
    fdp = descriptor_pb2.FileDescriptorProto()
    fdp.name = name
    fdp.package = spec["package"]
    fdp.syntax = "proto2"
    for enum_name, values in spec["enums"].items():
        enum = fdp.enum_type.add()
        enum.name = enum_name
        for i, value_name in enumerate(values):
            v = enum.value.add()
            v.name = value_name
            v.number = i
    return fdp


def closure(by_name, root, stubs):
    seen = set()
    missing = set()
    order = []  # dependency-first topological order

    def visit(name):
        if name in seen:
            return
        seen.add(name)
        if name in stubs:
            for dep_name in ():  # stubs declare no further dependencies
                visit(dep_name)
            order.append((name, stubs[name]))
            return
        if name not in by_name:
            missing.add(name)
            return
        fdp, _blob = by_name[name]
        for dep in fdp.dependency:
            visit(dep)
        order.append((name, fdp))

    visit(root)
    return order, missing


def main():
    binary_path = Path(sys.argv[1]) if len(sys.argv) > 1 else DEFAULT_BINARY
    root = (
        sys.argv[2]
        if len(sys.argv) > 2
        else "third_party/gemini_coder/proto/trajectory.proto"
    )
    out_path = Path(sys.argv[3]) if len(sys.argv) > 3 else Path("step.desc")

    by_name, n_candidates, n_fail = extract_all(binary_path)
    print(f"candidates: {n_candidates}  parsed ok: {len(by_name)}  failed: {n_fail}")

    stubs = {
        name: build_stub(name, spec) for name, spec in STUB_BROWSER_PROTO_DEPS.items()
    }
    order, missing = closure(by_name, root, stubs)
    if missing:
        print(f"UNRESOLVED dependencies (not stubbed): {sorted(missing)}")
        sys.exit(1)

    fdset = descriptor_pb2.FileDescriptorSet()
    for name, fdp in order:
        fdset.file.add().CopyFrom(fdp)

    out_path.write_bytes(fdset.SerializeToString())
    print(f"wrote {out_path} ({len(order)} files, {out_path.stat().st_size} bytes)")
    print("closure:", [name for name, _ in order])


if __name__ == "__main__":
    main()
