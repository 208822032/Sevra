#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import json
import socket
import sys
from collections import Counter, defaultdict
from dataclasses import dataclass, field, asdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional, Sequence, Tuple


DEFAULT_ADDR = "127.0.0.1:9300"
DEFAULT_NAMESPACE = "ai:chat:qa"
DEFAULT_PREVIEW_CHARS = 160
DEFAULT_VECTOR_PREVIEW = 6
DEFAULT_MEMBER_PREVIEW = 5

KNOWN_PREFIXES = [
    {
        "kind": "main_record",
        "prefix": "qa:",
        "label": "QA 主记录",
        "role": "保存问题、回答、作用域和元数据，是所有索引最终回表的实体。",
        "value": "JSON encoded llm.QARecord",
    },
    {
        "kind": "exact_index",
        "prefix": "qexact:",
        "label": "exact 索引",
        "role": "精确问句 hash -> question_id。",
        "value": "question_id string",
    },
    {
        "kind": "normalized_index",
        "prefix": "qnorm:",
        "label": "normalized 索引",
        "role": "归一化问句 hash -> question_id。",
        "value": "question_id string",
    },
    {
        "kind": "alias_index",
        "prefix": "qalias:",
        "label": "alias 索引",
        "role": "别名问法 hash -> canonical_question_id。",
        "value": "canonical question_id string",
    },
    {
        "kind": "candidate_index",
        "prefix": "qcand:",
        "label": "candidate 桶",
        "role": "scope + char-bigram 候选桶，用于 lexical recall / rerank。",
        "value": "JSON array of question_id strings",
    },
    {
        "kind": "semantic_embedding",
        "prefix": "qemb:",
        "label": "semantic embedding",
        "role": "单条问答的 embedding payload。",
        "value": "JSON encoded semanticEmbeddingPayload",
    },
    {
        "kind": "semantic_scope_index",
        "prefix": "qsemlist:",
        "label": "semantic scope list",
        "role": "scope 级语义候选集合，供 brute-force / ANN 构建候选池。",
        "value": "JSON array of question_id strings",
    },
    {
        "kind": "ann_persisted",
        "prefix": None,
        "label": "ANN 持久化 key",
        "role": "当前代码里没有持久化 ANN key 前缀；ANN 只存在于 KVLiteStore 的进程内缓存。",
        "value": "none",
    },
]


@dataclass
class RawDumpEntry:
    command: str
    key: str
    value: bytes
    expire_at_ms: Optional[int] = None


@dataclass
class DecodedItem:
    kind: str
    role: str
    raw_key: str
    logical_key: str
    storage_command: str
    scope: str
    expiry_at_ms: Optional[int]
    expiry_at: str
    data: Dict[str, Any] = field(default_factory=dict)


class RespParseError(RuntimeError):
    pass


class RespEOFError(RespParseError):
    pass


class RespReader:
    def __init__(self, data: bytes):
        self._data = data
        self._pos = 0

    def eof(self) -> bool:
        return self._pos >= len(self._data)

    def _read_exact(self, size: int) -> bytes:
        if self._pos + size > len(self._data):
            raise RespEOFError("unexpected EOF while reading RESP payload")
        chunk = self._data[self._pos:self._pos + size]
        self._pos += size
        return chunk

    def _read_line(self) -> bytes:
        end = self._data.find(b"\r\n", self._pos)
        if end < 0:
            raise RespEOFError("unexpected EOF while reading RESP line")
        line = self._data[self._pos:end]
        self._pos = end + 2
        return line

    def parse_value(self) -> Any:
        prefix = self._read_exact(1)
        if prefix == b"+":
            return self._read_line().decode("utf-8", errors="replace")
        if prefix == b"-":
            message = self._read_line().decode("utf-8", errors="replace")
            raise RespParseError(message)
        if prefix == b":":
            return int(self._read_line().decode("ascii", errors="strict"))
        if prefix == b"$":
            length = int(self._read_line().decode("ascii", errors="strict"))
            if length < 0:
                return None
            payload = self._read_exact(length)
            crlf = self._read_exact(2)
            if crlf != b"\r\n":
                raise RespParseError("invalid RESP bulk terminator")
            return payload
        if prefix == b"*":
            count = int(self._read_line().decode("ascii", errors="strict"))
            if count < 0:
                return None
            return [self.parse_value() for _ in range(count)]
        if prefix == b"_":
            _ = self._read_line()
            return None
        if prefix == b"#":
            value = self._read_line().decode("ascii", errors="strict")
            return value == "t"
        raise RespParseError(f"unsupported RESP prefix: {prefix!r}")

    def parse_stream(self) -> List[Any]:
        values: List[Any] = []
        while not self.eof():
            values.append(self.parse_value())
        return values


def normalize_namespace(namespace: str) -> str:
    cleaned = (namespace or "").strip().strip(":")
    if not cleaned:
        return DEFAULT_NAMESPACE
    if not cleaned.endswith(":qa"):
        cleaned = f"{cleaned}:qa"
    return cleaned


def namespace_prefix(namespace: str) -> str:
    return f"{normalize_namespace(namespace)}:"


def to_text(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return str(value)


def strip_namespace(raw_key: str, namespace: str) -> str:
    prefix = namespace_prefix(namespace)
    if raw_key.startswith(prefix):
        return raw_key[len(prefix):]
    return raw_key


def preview_text(value: Any, limit: int = DEFAULT_PREVIEW_CHARS) -> str:
    text = to_text(value).replace("\r", " ").replace("\n", " ").strip()
    if not text:
        return ""
    if len(text) <= limit:
        return text
    return text[: max(limit - 3, 1)] + "..."


def escape_cell(value: Any) -> str:
    text = to_text(value)
    text = text.replace("\\", "\\\\").replace("|", "\\|").replace("\n", " ").replace("\r", " ")
    return text


def format_human_ts(ts: Optional[int]) -> str:
    if ts is None or ts <= 0:
        return ""
    return datetime.fromtimestamp(ts, tz=timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")


def format_human_ts_ms(ts_ms: Optional[int]) -> str:
    if ts_ms is None or ts_ms <= 0:
        return ""
    return datetime.fromtimestamp(ts_ms / 1000.0, tz=timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")


def parse_int(value: Any) -> Optional[int]:
    if value is None:
        return None
    if isinstance(value, bool):
        return int(value)
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    text = to_text(value).strip()
    if not text:
        return None
    try:
        return int(text)
    except ValueError:
        return None


def parse_float(value: Any) -> Optional[float]:
    if value is None:
        return None
    if isinstance(value, bool):
        return float(value)
    if isinstance(value, (int, float)):
        return float(value)
    text = to_text(value).strip()
    if not text:
        return None
    try:
        return float(text)
    except ValueError:
        return None


def parse_bool(value: Any) -> Optional[bool]:
    if value is None:
        return None
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return bool(value)
    text = to_text(value).strip().lower()
    if not text:
        return None
    if text in {"1", "true", "yes", "y", "on"}:
        return True
    if text in {"0", "false", "no", "n", "off"}:
        return False
    return None


def parse_scope(scope: str) -> Dict[str, str]:
    result: Dict[str, str] = {}
    for part in (scope or "").split("|"):
        if "=" not in part:
            continue
        key, value = part.split("=", 1)
        if key and value:
            result[key.strip()] = value.strip()
    return result


def scope_display(scope: str) -> str:
    parts = parse_scope(scope)
    if not parts:
        return scope or ""
    ordered = []
    for key in ("provider", "model", "prompt_version"):
        if key in parts:
            ordered.append(f"{key}={parts[key]}")
    for key, value in parts.items():
        if key not in {"provider", "model", "prompt_version"}:
            ordered.append(f"{key}={value}")
    return " | ".join(ordered)


def question_id_scope(question_id: str) -> str:
    question_id = (question_id or "").strip()
    if ":" not in question_id:
        return ""
    return question_id.rsplit(":", 1)[0]


def question_id_hash(question_id: str) -> str:
    question_id = (question_id or "").strip()
    if ":" not in question_id:
        return ""
    return question_id.rsplit(":", 1)[1]


def decode_hex_signal(hex_signal: str) -> str:
    hex_signal = (hex_signal or "").strip()
    if not hex_signal:
        return ""
    try:
        raw = bytes.fromhex(hex_signal)
    except ValueError:
        return hex_signal
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError:
        return raw.decode("utf-8", errors="replace")


def json_loads_maybe(text: str) -> Tuple[Any, Optional[str]]:
    stripped = text.strip()
    if not stripped:
        return None, None
    try:
        return json.loads(stripped), None
    except json.JSONDecodeError as exc:
        return None, str(exc)


def dump_json_summary(value: Any, preview_limit: int = DEFAULT_PREVIEW_CHARS) -> Dict[str, Any]:
    if isinstance(value, dict):
        keys = list(value.keys())
        return {
            "type": "object",
            "size": len(value),
            "keys": keys[:10],
            "preview": preview_text(json.dumps(value, ensure_ascii=False), preview_limit),
        }
    if isinstance(value, list):
        return {
            "type": "array",
            "size": len(value),
            "preview": preview_text(json.dumps(value, ensure_ascii=False), preview_limit),
        }
    return {
        "type": type(value).__name__,
        "preview": preview_text(value, preview_limit),
    }


def maybe_base64_preview(value: bytes, limit: int = 48) -> str:
    if not value:
        return ""
    if len(value) <= limit:
        return value.decode("utf-8", errors="replace")
    return base64.b64encode(value[:limit]).decode("ascii") + "..."


def parse_raw_responses(data: bytes) -> List[Any]:
    reader = RespReader(data)
    values: List[Any] = []
    while not reader.eof():
        values.append(reader.parse_value())
    return values


def is_command_array(value: Any) -> bool:
    return isinstance(value, list) and len(value) >= 3


def decode_command_frame(frame: Any) -> Optional[RawDumpEntry]:
    if not is_command_array(frame):
        return None
    command = to_text(frame[0]).strip().upper()
    key = to_text(frame[1])
    value_item = frame[2]
    if isinstance(value_item, bytes):
        value = value_item
    elif value_item is None:
        value = b""
    else:
        value = to_text(value_item).encode("utf-8", errors="replace")
    if command == "PEXPIREAT":
        expire_at_ms = parse_int(value_item)
        if expire_at_ms is None:
            raise RespParseError(f"invalid PEXPIREAT payload for key {key!r}")
        return RawDumpEntry(command=command, key=key, value=value, expire_at_ms=expire_at_ms)
    return RawDumpEntry(command=command, key=key, value=value)


def load_raw_dump_bytes(source: str, input_path: Optional[str], addr: str, timeout: float, save_raw: Optional[str]) -> bytes:
    source = (source or "direct").strip().lower()
    if source == "file":
        if not input_path:
            raise ValueError("--input is required when --source=file")
        return Path(input_path).read_bytes()
    host, port = parse_addr(addr)
    payload = fetch_full_dump_from_server(host, port, timeout)
    if save_raw:
        Path(save_raw).write_bytes(payload)
    return payload


def parse_addr(addr: str) -> Tuple[str, int]:
    cleaned = (addr or "").strip()
    if ":" not in cleaned:
        raise ValueError(f"invalid addr {addr!r}; expected host:port")
    host, port_text = cleaned.rsplit(":", 1)
    port = int(port_text)
    return host, port


def build_resp_command(parts: Sequence[str]) -> bytes:
    chunks = [f"*{len(parts)}\r\n".encode("ascii")]
    for part in parts:
        encoded = part.encode("utf-8")
        chunks.append(f"${len(encoded)}\r\n".encode("ascii"))
        chunks.append(encoded)
        chunks.append(b"\r\n")
    return b"".join(chunks)


def fetch_full_dump_from_server(host: str, port: int, timeout: float) -> bytes:
    sock = socket.create_connection((host, port), timeout=timeout)
    try:
        sock.settimeout(timeout)
        # 只发 FULLDUMP，不额外发送 QUIT；直接读完整个响应即可。
        sock.sendall(build_resp_command(["FULLDUMP"]))

        chunks: List[bytes] = []
        received_any = False
        while True:
            try:
                chunk = sock.recv(65536)
            except socket.timeout as exc:
                if received_any:
                    break
                raise RespParseError(f"timed out waiting for FULLDUMP response from {host}:{port}") from exc

            if not chunk:
                break
            received_any = True
            chunks.append(chunk)

        payload = b"".join(chunks)
        return payload
    finally:
        try:
            sock.close()
        except OSError:
            pass


def decode_metadata_map(raw: Dict[str, Any]) -> Dict[str, str]:
    metadata = raw.get("metadata")
    if not isinstance(metadata, dict):
        return {}
    decoded: Dict[str, str] = {}
    for key, value in metadata.items():
        if value is None:
            continue
        decoded[str(key)] = to_text(value)
    return decoded


def pick_str(raw: Dict[str, Any], metadata: Dict[str, str], key: str, fallback: str = "") -> str:
    value = raw.get(key)
    if value is None or value == "":
        value = metadata.get(key, fallback)
    return to_text(value).strip()


def pick_int(raw: Dict[str, Any], metadata: Dict[str, str], key: str) -> Optional[int]:
    value = raw.get(key)
    if value is None or value == "":
        value = metadata.get(key)
    return parse_int(value)


def pick_float(raw: Dict[str, Any], metadata: Dict[str, str], key: str) -> Optional[float]:
    value = raw.get(key)
    if value is None or value == "":
        value = metadata.get(key)
    return parse_float(value)


def pick_bool(raw: Dict[str, Any], metadata: Dict[str, str], key: str) -> Optional[bool]:
    value = raw.get(key)
    if value is None or value == "":
        value = metadata.get(key)
    return parse_bool(value)


def decode_main_record(raw: RawDumpEntry, namespace: str, decoded_payload: Dict[str, Any]) -> DecodedItem:
    metadata = decode_metadata_map(decoded_payload)
    question_id = pick_str(decoded_payload, metadata, "question_id")
    if not question_id:
        logical = strip_namespace(raw.key, namespace)
        if logical.startswith("qa:"):
            question_id = logical[len("qa:"):]
    scope = pick_str(decoded_payload, metadata, "scope")
    if not scope:
        scope = question_id_scope(question_id)
    data = {
        "question_id": question_id,
        "record_id": pick_str(decoded_payload, metadata, "record_id"),
        "canonical_question_id": pick_str(decoded_payload, metadata, "canonical_question_id"),
        "session_id": pick_str(decoded_payload, metadata, "session_id"),
        "turn_seq": pick_int(decoded_payload, metadata, "turn_seq"),
        "request_message_id": pick_str(decoded_payload, metadata, "request_message_id"),
        "parent_message_id": pick_str(decoded_payload, metadata, "parent_message_id"),
        "question": pick_str(decoded_payload, metadata, "question"),
        "raw_question": pick_str(decoded_payload, metadata, "raw_question"),
        "canonical_question": pick_str(decoded_payload, metadata, "canonical_question"),
        "question_hash": pick_str(decoded_payload, metadata, "question_hash"),
        "raw_question_hash": pick_str(decoded_payload, metadata, "raw_question_hash"),
        "normalized_question": pick_str(decoded_payload, metadata, "normalized_question"),
        "answer": pick_str(decoded_payload, metadata, "answer"),
        "answer_summary": pick_str(decoded_payload, metadata, "answer_summary"),
        "scope": scope,
        "scope_parts": parse_scope(scope),
        "prompt_version": pick_str(decoded_payload, metadata, "prompt_version"),
        "source_type": pick_str(decoded_payload, metadata, "source_type"),
        "hit_type": pick_str(decoded_payload, metadata, "hit_type"),
        "hit_count": pick_int(decoded_payload, metadata, "hit_count"),
        "last_hit_at": pick_int(decoded_payload, metadata, "last_hit_at"),
        "provider": pick_str(decoded_payload, metadata, "provider"),
        "model": pick_str(decoded_payload, metadata, "model"),
        "embedding_model": pick_str(decoded_payload, metadata, "embedding_model"),
        "embedding_status": pick_str(decoded_payload, metadata, "embedding_status"),
        "embedding_dim": pick_int(decoded_payload, metadata, "embedding_dim"),
        "embedding_updated_at": pick_int(decoded_payload, metadata, "embedding_updated_at"),
        "embedding_ref": pick_str(decoded_payload, metadata, "embedding_ref"),
        "semantic_ready": pick_bool(decoded_payload, metadata, "semantic_ready"),
        "similarity_score": pick_float(decoded_payload, metadata, "similarity_score"),
        "origin_record_id": pick_str(decoded_payload, metadata, "origin_record_id"),
        "created_at": pick_int(decoded_payload, metadata, "created_at"),
        "updated_at": pick_int(decoded_payload, metadata, "updated_at"),
        "expires_at": pick_int(decoded_payload, metadata, "expires_at"),
        "created_at_human": format_human_ts(pick_int(decoded_payload, metadata, "created_at")),
        "updated_at_human": format_human_ts(pick_int(decoded_payload, metadata, "updated_at")),
        "expires_at_human": format_human_ts(pick_int(decoded_payload, metadata, "expires_at")),
        "metadata": metadata,
        "prompt_tokens": pick_int(decoded_payload, metadata, "prompt_tokens"),
        "completion_tokens": pick_int(decoded_payload, metadata, "completion_tokens"),
        "total_tokens": pick_int(decoded_payload, metadata, "total_tokens"),
        "answer_source_kind": pick_str(decoded_payload, metadata, "answer_source_kind"),
        "qa_hit_type": pick_str(decoded_payload, metadata, "qa_hit_type"),
        "semantic_mode_used": pick_str(decoded_payload, metadata, "semantic_mode_used"),
        "semantic_fallback": pick_bool(decoded_payload, metadata, "semantic_fallback"),
        "semantic_fallback_reason": pick_str(decoded_payload, metadata, "semantic_fallback_reason"),
        "answer_source": pick_str(decoded_payload, metadata, "answer_source"),
    }
    return DecodedItem(
        kind="main_record",
        role="QA 主记录",
        raw_key=raw.key,
        logical_key=strip_namespace(raw.key, namespace),
        storage_command=raw.command,
        scope=scope,
        expiry_at_ms=raw.expire_at_ms,
        expiry_at=format_human_ts_ms(raw.expire_at_ms),
        data=data,
    )


def decode_exact_like_record(raw: RawDumpEntry, namespace: str, kind: str, role: str, target_label: str) -> DecodedItem:
    logical = strip_namespace(raw.key, namespace)
    remainder = logical[len(target_label):] if logical.startswith(target_label) else logical
    scope = ""
    hash_value = ""
    if ":" in remainder:
        scope, hash_value = remainder.split(":", 1)
    else:
        scope = remainder
    target_question_id = to_text(raw.value).strip()
    data = {
        "scope": scope,
        "scope_parts": parse_scope(scope),
        "hash": hash_value,
        "target_question_id": target_question_id,
        "target_question_scope": question_id_scope(target_question_id),
        "target_question_hash": question_id_hash(target_question_id),
        "target_question": "",
        "target_answer": "",
        "target_exists": False,
        "target_preview": "",
    }
    return DecodedItem(
        kind=kind,
        role=role,
        raw_key=raw.key,
        logical_key=logical,
        storage_command=raw.command,
        scope=scope,
        expiry_at_ms=raw.expire_at_ms,
        expiry_at=format_human_ts_ms(raw.expire_at_ms),
        data=data,
    )


def decode_alias_record(raw: RawDumpEntry, namespace: str) -> DecodedItem:
    logical = strip_namespace(raw.key, namespace)
    remainder = logical[len("qalias:"):] if logical.startswith("qalias:") else logical
    scope = ""
    alias_hash = ""
    if ":" in remainder:
        scope, alias_hash = remainder.split(":", 1)
    else:
        scope = remainder
    canonical_question_id = to_text(raw.value).strip()
    data = {
        "scope": scope,
        "scope_parts": parse_scope(scope),
        "alias_hash": alias_hash,
        "canonical_question_id": canonical_question_id,
        "canonical_question_scope": question_id_scope(canonical_question_id),
        "canonical_question_hash": question_id_hash(canonical_question_id),
        "canonical_question": "",
        "canonical_answer": "",
        "target_exists": False,
        "target_preview": "",
    }
    return DecodedItem(
        kind="alias_index",
        role="alias 索引",
        raw_key=raw.key,
        logical_key=logical,
        storage_command=raw.command,
        scope=scope,
        expiry_at_ms=raw.expire_at_ms,
        expiry_at=format_human_ts_ms(raw.expire_at_ms),
        data=data,
    )


def decode_candidate_record(raw: RawDumpEntry, namespace: str) -> DecodedItem:
    logical = strip_namespace(raw.key, namespace)
    remainder = logical[len("qcand:"):] if logical.startswith("qcand:") else logical
    scope = remainder
    signal_hex = ""
    if ":bg:" in remainder:
        scope, signal_hex = remainder.split(":bg:", 1)
    signal_text = decode_hex_signal(signal_hex)
    payload, error = json_loads_maybe(to_text(raw.value))
    member_ids: List[str] = []
    if isinstance(payload, list):
        member_ids = [to_text(item).strip() for item in payload if to_text(item).strip()]
    data = {
        "scope": scope,
        "scope_parts": parse_scope(scope),
        "signal_hex": signal_hex,
        "signal_text": signal_text,
        "member_question_ids": member_ids,
        "member_count": len(member_ids),
        "member_sample": member_ids[:DEFAULT_MEMBER_PREVIEW],
        "json_error": error,
    }
    if error is None and isinstance(payload, list):
        data["payload_summary"] = dump_json_summary(payload)
    return DecodedItem(
        kind="candidate_index",
        role="candidate 桶",
        raw_key=raw.key,
        logical_key=logical,
        storage_command=raw.command,
        scope=scope,
        expiry_at_ms=raw.expire_at_ms,
        expiry_at=format_human_ts_ms(raw.expire_at_ms),
        data=data,
    )


def decode_semantic_embedding_record(raw: RawDumpEntry, namespace: str) -> DecodedItem:
    logical = strip_namespace(raw.key, namespace)
    question_id = logical[len("qemb:"):] if logical.startswith("qemb:") else logical
    payload, error = json_loads_maybe(to_text(raw.value))
    vector = []
    model = ""
    provider = ""
    dim = None
    updated_at = None
    if isinstance(payload, dict):
        vector = payload.get("vector") if isinstance(payload.get("vector"), list) else []
        model = to_text(payload.get("model")).strip()
        provider = to_text(payload.get("provider")).strip()
        dim = parse_int(payload.get("dim"))
        updated_at = parse_int(payload.get("updated_at"))
    data = {
        "question_id": question_id,
        "question_scope": question_id_scope(question_id),
        "question_hash": question_id_hash(question_id),
        "model": model,
        "provider": provider,
        "dim": dim,
        "vector_length": len(vector),
        "vector_preview": vector[:DEFAULT_VECTOR_PREVIEW],
        "updated_at": updated_at,
        "updated_at_human": format_human_ts(updated_at),
        "ready": bool(vector),
        "payload_summary": dump_json_summary(payload) if error is None and isinstance(payload, dict) else None,
        "json_error": error,
    }
    return DecodedItem(
        kind="semantic_embedding",
        role="semantic embedding",
        raw_key=raw.key,
        logical_key=logical,
        storage_command=raw.command,
        scope=question_id_scope(question_id),
        expiry_at_ms=raw.expire_at_ms,
        expiry_at=format_human_ts_ms(raw.expire_at_ms),
        data=data,
    )


def decode_semantic_scope_record(raw: RawDumpEntry, namespace: str) -> DecodedItem:
    logical = strip_namespace(raw.key, namespace)
    scope = logical[len("qsemlist:"):] if logical.startswith("qsemlist:") else logical
    payload, error = json_loads_maybe(to_text(raw.value))
    member_ids: List[str] = []
    if isinstance(payload, list):
        member_ids = [to_text(item).strip() for item in payload if to_text(item).strip()]
    data = {
        "scope": scope,
        "scope_parts": parse_scope(scope),
        "member_question_ids": member_ids,
        "member_count": len(member_ids),
        "member_sample": member_ids[:DEFAULT_MEMBER_PREVIEW],
        "json_error": error,
        "payload_summary": dump_json_summary(payload) if error is None and isinstance(payload, list) else None,
    }
    return DecodedItem(
        kind="semantic_scope_index",
        role="semantic scope list",
        raw_key=raw.key,
        logical_key=logical,
        storage_command=raw.command,
        scope=scope,
        expiry_at_ms=raw.expire_at_ms,
        expiry_at=format_human_ts_ms(raw.expire_at_ms),
        data=data,
    )


def decode_unknown_record(raw: RawDumpEntry, namespace: str) -> DecodedItem:
    logical = strip_namespace(raw.key, namespace)
    value_text = to_text(raw.value)
    payload, error = json_loads_maybe(value_text)
    data = {
        "value_preview": preview_text(value_text),
        "value_length": len(raw.value),
        "raw_value_b64_preview": maybe_base64_preview(raw.value),
        "json_error": error,
        "json_summary": dump_json_summary(payload) if error is None else None,
    }
    return DecodedItem(
        kind="unknown",
        role="未识别 key",
        raw_key=raw.key,
        logical_key=logical,
        storage_command=raw.command,
        scope="",
        expiry_at_ms=raw.expire_at_ms,
        expiry_at=format_human_ts_ms(raw.expire_at_ms),
        data=data,
    )


def classify_raw_entry(raw: RawDumpEntry, namespace: str) -> DecodedItem:
    logical = strip_namespace(raw.key, namespace)
    if logical.startswith("qa:") or raw.key.startswith("qa:"):
        payload, error = json_loads_maybe(to_text(raw.value))
        if error is not None or not isinstance(payload, dict):
            return decode_unknown_record(raw, namespace)
        return decode_main_record(raw, namespace, payload)
    if logical.startswith("qexact:") or raw.key.startswith("qexact:"):
        return decode_exact_like_record(raw, namespace, "exact_index", "exact 索引", "qexact:")
    if logical.startswith("qnorm:") or raw.key.startswith("qnorm:"):
        return decode_exact_like_record(raw, namespace, "normalized_index", "normalized 索引", "qnorm:")
    if logical.startswith("qalias:") or raw.key.startswith("qalias:"):
        return decode_alias_record(raw, namespace)
    if logical.startswith("qcand:") or raw.key.startswith("qcand:"):
        return decode_candidate_record(raw, namespace)
    if logical.startswith("qemb:") or raw.key.startswith("qemb:"):
        return decode_semantic_embedding_record(raw, namespace)
    if logical.startswith("qsemlist:") or raw.key.startswith("qsemlist:"):
        return decode_semantic_scope_record(raw, namespace)
    return decode_unknown_record(raw, namespace)


def build_decoded_items(raw_entries: List[RawDumpEntry], namespace: str) -> Tuple[List[DecodedItem], Dict[str, int]]:
    decoded_items = [classify_raw_entry(entry, namespace) for entry in raw_entries if entry.command != "PEXPIREAT"]
    expiry_map = {entry.key: entry.expire_at_ms for entry in raw_entries if entry.command == "PEXPIREAT" and entry.expire_at_ms is not None}
    for item in decoded_items:
        if item.raw_key in expiry_map:
            item.expiry_at_ms = expiry_map[item.raw_key]
            item.expiry_at = format_human_ts_ms(item.expiry_at_ms)
    return decoded_items, expiry_map


def build_lookup_maps(items: List[DecodedItem]) -> Dict[str, Dict[str, List[DecodedItem]]]:
    maps: Dict[str, Dict[str, List[DecodedItem]]] = {
        "exact": defaultdict(list),
        "normalized": defaultdict(list),
        "alias": defaultdict(list),
        "candidate": defaultdict(list),
        "semantic_scope": defaultdict(list),
        "semantic_embedding": defaultdict(list),
        "main_by_question_id": defaultdict(list),
        "by_raw_key": defaultdict(list),
    }
    for item in items:
        maps["by_raw_key"][item.raw_key].append(item)
        if item.kind == "main_record":
            qid = to_text(item.data.get("question_id")).strip()
            if qid:
                maps["main_by_question_id"][qid].append(item)
        elif item.kind == "exact_index":
            target = to_text(item.data.get("target_question_id")).strip()
            if target:
                maps["exact"][target].append(item)
        elif item.kind == "normalized_index":
            target = to_text(item.data.get("target_question_id")).strip()
            if target:
                maps["normalized"][target].append(item)
        elif item.kind == "alias_index":
            target = to_text(item.data.get("canonical_question_id")).strip()
            if target:
                maps["alias"][target].append(item)
        elif item.kind == "candidate_index":
            for question_id in item.data.get("member_question_ids", []):
                maps["candidate"][to_text(question_id).strip()].append(item)
        elif item.kind == "semantic_scope_index":
            for question_id in item.data.get("member_question_ids", []):
                maps["semantic_scope"][to_text(question_id).strip()].append(item)
        elif item.kind == "semantic_embedding":
            target = to_text(item.data.get("question_id")).strip()
            if target:
                maps["semantic_embedding"][target].append(item)
    return maps


def enrich_items(items: List[DecodedItem], maps: Dict[str, Dict[str, List[DecodedItem]]]) -> None:
    main_lookup = maps["main_by_question_id"]
    for item in items:
        if item.kind == "main_record":
            question_id = to_text(item.data.get("question_id")).strip()
            exact_items = maps["exact"].get(question_id, [])
            normalized_items = maps["normalized"].get(question_id, [])
            alias_items = maps["alias"].get(question_id, [])
            candidate_items = maps["candidate"].get(question_id, [])
            semantic_scope_items = maps["semantic_scope"].get(question_id, [])
            semantic_embedding_items = maps["semantic_embedding"].get(question_id, [])
            if semantic_embedding_items:
                payload = semantic_embedding_items[0].data
                item.data["semantic_payload"] = payload
                item.data["has_semantic_vector"] = bool(payload.get("vector_length", 0))
            else:
                item.data["semantic_payload"] = None
                item.data["has_semantic_vector"] = bool(item.data.get("semantic_ready")) or to_text(item.data.get("embedding_status")).lower() == "ready"
            item.data["has_exact_index"] = bool(exact_items)
            item.data["has_normalized_index"] = bool(normalized_items)
            item.data["has_alias_index"] = bool(alias_items)
            item.data["has_candidate_index"] = bool(candidate_items)
            item.data["has_semantic_scope_index"] = bool(semantic_scope_items)
            item.data["exact_index_keys"] = [i.raw_key for i in exact_items]
            item.data["normalized_index_keys"] = [i.raw_key for i in normalized_items]
            item.data["alias_index_keys"] = [i.raw_key for i in alias_items]
            item.data["candidate_bucket_keys"] = [i.raw_key for i in candidate_items]
            item.data["semantic_scope_keys"] = [i.raw_key for i in semantic_scope_items]
            item.data["semantic_embedding_key"] = semantic_embedding_items[0].raw_key if semantic_embedding_items else ""
            lookup_modes = []
            if exact_items:
                lookup_modes.append("exact")
            if normalized_items:
                lookup_modes.append("normalized")
            if alias_items:
                lookup_modes.append("alias")
            if candidate_items:
                lookup_modes.append("candidate")
            if semantic_scope_items or semantic_embedding_items:
                lookup_modes.append("semantic")
            item.data["lookup_modes_possible"] = lookup_modes
            item.data["system_role"] = build_main_system_role(item)
            item.data["role_explanation"] = build_main_role_explanation(item)
            item.data["coverage_summary"] = build_main_coverage_summary(item)
        elif item.kind == "exact_index":
            target = to_text(item.data.get("target_question_id")).strip()
            target_items = main_lookup.get(target, [])
            item.data["target_exists"] = bool(target_items)
            if target_items:
                target_item = target_items[0]
                item.data["target_question"] = preview_text(target_item.data.get("question"), DEFAULT_PREVIEW_CHARS)
                item.data["target_answer"] = preview_text(target_item.data.get("answer"), DEFAULT_PREVIEW_CHARS)
                item.data["target_preview"] = f"{preview_text(target_item.data.get('question'), 64)} -> {preview_text(target_item.data.get('answer'), 96)}"
            item.data["system_role"] = "exact hash 索引，命中后回表主记录。"
        elif item.kind == "normalized_index":
            target = to_text(item.data.get("target_question_id")).strip()
            target_items = main_lookup.get(target, [])
            item.data["target_exists"] = bool(target_items)
            if target_items:
                target_item = target_items[0]
                item.data["target_question"] = preview_text(target_item.data.get("question"), DEFAULT_PREVIEW_CHARS)
                item.data["target_answer"] = preview_text(target_item.data.get("answer"), DEFAULT_PREVIEW_CHARS)
                item.data["target_preview"] = f"{preview_text(target_item.data.get('question'), 64)} -> {preview_text(target_item.data.get('answer'), 96)}"
            item.data["system_role"] = "normalized hash 索引，允许标点/空白差异后回表主记录。"
        elif item.kind == "alias_index":
            target = to_text(item.data.get("canonical_question_id")).strip()
            target_items = main_lookup.get(target, [])
            item.data["target_exists"] = bool(target_items)
            if target_items:
                target_item = target_items[0]
                item.data["canonical_question"] = preview_text(target_item.data.get("canonical_question"), DEFAULT_PREVIEW_CHARS)
                item.data["canonical_answer"] = preview_text(target_item.data.get("answer"), DEFAULT_PREVIEW_CHARS)
                item.data["target_preview"] = f"{preview_text(target_item.data.get('canonical_question'), 64)} -> {preview_text(target_item.data.get('answer'), 96)}"
            item.data["system_role"] = "别名问法索引，先映射到 canonical_question_id，再回表主记录。"
        elif item.kind == "candidate_index":
            members = [to_text(x).strip() for x in item.data.get("member_question_ids", []) if to_text(x).strip()]
            samples = []
            for question_id in members[:DEFAULT_MEMBER_PREVIEW]:
                candidates = main_lookup.get(question_id, [])
                if candidates:
                    candidate = candidates[0]
                    samples.append({
                        "question_id": question_id,
                        "question": preview_text(candidate.data.get("question"), 72),
                        "answer": preview_text(candidate.data.get("answer"), 88),
                    })
                else:
                    samples.append({"question_id": question_id, "question": "", "answer": ""})
            item.data["member_question_ids"] = members
            item.data["member_count"] = len(members)
            item.data["member_sample"] = members[:DEFAULT_MEMBER_PREVIEW]
            item.data["member_targets_sample"] = samples
            item.data["known_target_count"] = sum(1 for question_id in members if question_id in main_lookup)
            item.data["system_role"] = "char-bigram 候选桶，给 lexical recall / rerank 提供候选池。"
        elif item.kind == "semantic_embedding":
            question_id = to_text(item.data.get("question_id")).strip()
            target_items = main_lookup.get(question_id, [])
            item.data["target_exists"] = bool(target_items)
            if target_items:
                target_item = target_items[0]
                item.data["target_question"] = preview_text(target_item.data.get("question"), DEFAULT_PREVIEW_CHARS)
                item.data["target_answer"] = preview_text(target_item.data.get("answer"), DEFAULT_PREVIEW_CHARS)
            item.data["system_role"] = "语义向量 payload，本身不是 ANN 索引，但会被 brute-force / ANN 读取。"
        elif item.kind == "semantic_scope_index":
            members = [to_text(x).strip() for x in item.data.get("member_question_ids", []) if to_text(x).strip()]
            samples = []
            for question_id in members[:DEFAULT_MEMBER_PREVIEW]:
                candidates = main_lookup.get(question_id, [])
                if candidates:
                    candidate = candidates[0]
                    samples.append({
                        "question_id": question_id,
                        "question": preview_text(candidate.data.get("question"), 72),
                        "answer": preview_text(candidate.data.get("answer"), 88),
                    })
                else:
                    samples.append({"question_id": question_id, "question": "", "answer": ""})
            item.data["member_question_ids"] = members
            item.data["member_count"] = len(members)
            item.data["member_sample"] = members[:DEFAULT_MEMBER_PREVIEW]
            item.data["member_targets_sample"] = samples
            item.data["known_target_count"] = sum(1 for question_id in members if question_id in main_lookup)
            item.data["system_role"] = "scope 级语义候选列表，供 brute-force 语义召回和 ANN 索引构建候选池。"
        else:
            item.data["system_role"] = "未识别 key，保留原始值供人工检查。"


def build_main_system_role(item: DecodedItem) -> str:
    parts = ["QA 主记录，保存问题、回答、作用域和回表需要的元数据。"]
    if item.data.get("has_exact_index"):
        parts.append("exact 索引会用 raw_question_hash 回表它。")
    if item.data.get("has_normalized_index"):
        parts.append("normalized 索引会用 question_hash 回表它。")
    if item.data.get("has_alias_index"):
        parts.append("alias 索引会把别名问法映射到它。")
    if item.data.get("has_candidate_index"):
        parts.append("candidate 桶会把相近问法召回到它。")
    if item.data.get("has_semantic_scope_index") or item.data.get("has_semantic_vector"):
        parts.append("semantic 数据让它进入语义召回候选集。")
    return " ".join(parts)


def build_main_role_explanation(item: DecodedItem) -> str:
    scope = item.data.get("scope") or ""
    scope_bits = parse_scope(scope)
    provider = scope_bits.get("provider", item.data.get("provider", ""))
    model = scope_bits.get("model", item.data.get("model", ""))
    prompt_version = scope_bits.get("prompt_version", item.data.get("prompt_version", ""))
    summary = [
        f"作用范围是 {scope_display(scope) or scope or '(empty)'}。",
        f"它属于 provider={provider or 'n/a'} / model={model or 'n/a'} / prompt_version={prompt_version or 'n/a'} 这一组。",
        "主记录是业务层真正可展示的问答内容，索引只负责定位它。",
    ]
    if item.data.get("has_semantic_vector"):
        summary.append("当前这条记录还带有 semantic vector 或 semantic_ready 标记，可以参与语义检索。")
    if item.data.get("answer_source"):
        summary.append(f"answer_source = {item.data.get('answer_source')}，说明它的答案来源标签是持久化在 metadata 里的。")
    return " ".join(summary)


def build_main_coverage_summary(item: DecodedItem) -> Dict[str, Any]:
    return {
        "exact": bool(item.data.get("has_exact_index")),
        "normalized": bool(item.data.get("has_normalized_index")),
        "alias": bool(item.data.get("has_alias_index")),
        "candidate": bool(item.data.get("has_candidate_index")),
        "semantic_scope": bool(item.data.get("has_semantic_scope_index")),
        "semantic_vector": bool(item.data.get("has_semantic_vector")),
        "lookup_modes_possible": list(item.data.get("lookup_modes_possible", [])),
    }


def item_matches_filters(item: DecodedItem, filters: Dict[str, Any]) -> bool:
    if filters.get("question_id"):
        qid = filters["question_id"]
        if item.kind == "main_record":
            if to_text(item.data.get("question_id")).strip() != qid:
                return False
        elif item.kind in {"exact_index", "normalized_index"}:
            if to_text(item.data.get("target_question_id")).strip() != qid:
                return False
        elif item.kind == "alias_index":
            if to_text(item.data.get("canonical_question_id")).strip() != qid:
                return False
        elif item.kind == "candidate_index":
            if qid not in {to_text(x).strip() for x in item.data.get("member_question_ids", [])}:
                return False
        elif item.kind == "semantic_embedding":
            if to_text(item.data.get("question_id")).strip() != qid:
                return False
        elif item.kind == "semantic_scope_index":
            if qid not in {to_text(x).strip() for x in item.data.get("member_question_ids", [])}:
                return False
        elif item.kind == "unknown":
            if qid not in item.raw_key and qid not in item.logical_key:
                return False
    if filters.get("key"):
        if item.raw_key != filters["key"] and item.logical_key != filters["key"]:
            return False
    if filters.get("scope"):
        scope = filters["scope"]
        item_scope = item.scope or to_text(item.data.get("scope")).strip()
        if item.kind == "main_record":
            item_scope = to_text(item.data.get("scope")).strip()
        if item_scope != scope:
            return False
    if filters.get("semantic_only"):
        if item.kind == "main_record":
            if not bool(item.data.get("has_semantic_vector")):
                return False
        elif item.kind not in {"semantic_embedding", "semantic_scope_index"}:
            return False
    if filters.get("unknown_only") and item.kind != "unknown":
        return False
    selected_kinds = filters.get("selected_kinds")
    if selected_kinds and item.kind not in selected_kinds:
        return False
    return True


def apply_kind_aliases(selected: List[str]) -> List[str]:
    aliases = {
        "index": {"exact_index", "normalized_index", "alias_index", "candidate_index", "semantic_embedding", "semantic_scope_index"},
        "semantic": {"semantic_embedding", "semantic_scope_index"},
        "main": {"main_record"},
        "unknown": {"unknown"},
        "all": {"main_record", "exact_index", "normalized_index", "alias_index", "candidate_index", "semantic_embedding", "semantic_scope_index", "unknown"},
    }
    result: List[str] = []
    for kind in selected:
        if kind in aliases:
            for expanded in aliases[kind]:
                if expanded not in result:
                    result.append(expanded)
        elif kind not in result:
            result.append(kind)
    return result


def build_target_explanation(target: Optional[DecodedItem], items: List[DecodedItem], maps: Dict[str, Dict[str, List[DecodedItem]]], question_id: str = "") -> Dict[str, Any]:
    if target is None:
        related = []
        if question_id:
            for item in items:
                if item.kind == "main_record" and to_text(item.data.get("question_id")).strip() == question_id:
                    target = item
                    break
        if target is None and question_id:
            for item in items:
                if item.kind in {"exact_index", "normalized_index"} and to_text(item.data.get("target_question_id")).strip() == question_id:
                    related.append(item)
                elif item.kind == "alias_index" and to_text(item.data.get("canonical_question_id")).strip() == question_id:
                    related.append(item)
                elif item.kind == "candidate_index" and question_id in {to_text(x).strip() for x in item.data.get("member_question_ids", [])}:
                    related.append(item)
                elif item.kind == "semantic_embedding" and to_text(item.data.get("question_id")).strip() == question_id:
                    related.append(item)
                elif item.kind == "semantic_scope_index" and question_id in {to_text(x).strip() for x in item.data.get("member_question_ids", [])}:
                    related.append(item)
        return {
            "status": "not_found",
            "question_id": question_id,
            "related_entries": [item_to_dict(x, preview_only=True) for x in related],
            "message": "没有找到对应主记录，只能展示相关索引命中痕迹。",
        }
    qid = to_text(target.data.get("question_id")).strip() if target.kind == "main_record" else ""
    explanation = {
        "status": "ok",
        "kind": target.kind,
        "raw_key": target.raw_key,
        "logical_key": target.logical_key,
        "role": target.role,
        "system_role": target.data.get("system_role", target.role),
        "role_explanation": target.data.get("role_explanation", ""),
        "coverage_summary": target.data.get("coverage_summary", {}),
        "relations": {},
        "payload": item_to_dict(target, preview_only=False),
    }
    if target.kind == "main_record":
        explanation["relations"] = {
            "exact_index_keys": target.data.get("exact_index_keys", []),
            "normalized_index_keys": target.data.get("normalized_index_keys", []),
            "alias_index_keys": target.data.get("alias_index_keys", []),
            "candidate_bucket_keys": target.data.get("candidate_bucket_keys", []),
            "semantic_embedding_key": target.data.get("semantic_embedding_key", ""),
            "semantic_scope_keys": target.data.get("semantic_scope_keys", []),
            "lookup_modes_possible": target.data.get("lookup_modes_possible", []),
        }
        explanation["linked_indexes"] = {
            "exact": [item_to_dict(x, preview_only=True) for x in maps["exact"].get(qid, [])],
            "normalized": [item_to_dict(x, preview_only=True) for x in maps["normalized"].get(qid, [])],
            "alias": [item_to_dict(x, preview_only=True) for x in maps["alias"].get(qid, [])],
            "candidate": [item_to_dict(x, preview_only=True) for x in maps["candidate"].get(qid, [])],
            "semantic_embedding": [item_to_dict(x, preview_only=True) for x in maps["semantic_embedding"].get(qid, [])],
            "semantic_scope": [item_to_dict(x, preview_only=True) for x in maps["semantic_scope"].get(qid, [])],
        }
    else:
        explanation["relations"] = derive_index_relations_for_item(target, maps)
    return explanation


def derive_index_relations_for_item(item: DecodedItem, maps: Dict[str, Dict[str, List[DecodedItem]]]) -> Dict[str, Any]:
    if item.kind == "exact_index":
        qid = to_text(item.data.get("target_question_id")).strip()
        return {
            "target_question_id": qid,
            "target_record": [item_to_dict(x, preview_only=True) for x in maps["main_by_question_id"].get(qid, [])],
            "role": "exact hash -> question_id -> main record",
        }
    if item.kind == "normalized_index":
        qid = to_text(item.data.get("target_question_id")).strip()
        return {
            "target_question_id": qid,
            "target_record": [item_to_dict(x, preview_only=True) for x in maps["main_by_question_id"].get(qid, [])],
            "role": "normalized hash -> question_id -> main record",
        }
    if item.kind == "alias_index":
        qid = to_text(item.data.get("canonical_question_id")).strip()
        return {
            "canonical_question_id": qid,
            "canonical_record": [item_to_dict(x, preview_only=True) for x in maps["main_by_question_id"].get(qid, [])],
            "role": "alias hash -> canonical_question_id -> main record",
        }
    if item.kind == "candidate_index":
        members = [to_text(x).strip() for x in item.data.get("member_question_ids", []) if to_text(x).strip()]
        known = [qid for qid in members if qid in maps["main_by_question_id"]]
        return {
            "member_count": len(members),
            "known_target_count": len(known),
            "member_sample": members[:DEFAULT_MEMBER_PREVIEW],
            "known_target_sample": known[:DEFAULT_MEMBER_PREVIEW],
            "role": "candidate bucket -> main records",
        }
    if item.kind == "semantic_embedding":
        qid = to_text(item.data.get("question_id")).strip()
        return {
            "question_id": qid,
            "target_record": [item_to_dict(x, preview_only=True) for x in maps["main_by_question_id"].get(qid, [])],
            "role": "embedding payload -> main record",
        }
    if item.kind == "semantic_scope_index":
        members = [to_text(x).strip() for x in item.data.get("member_question_ids", []) if to_text(x).strip()]
        known = [qid for qid in members if qid in maps["main_by_question_id"]]
        return {
            "member_count": len(members),
            "known_target_count": len(known),
            "member_sample": members[:DEFAULT_MEMBER_PREVIEW],
            "known_target_sample": known[:DEFAULT_MEMBER_PREVIEW],
            "role": "semantic scope list -> brute-force / ANN candidate pool",
        }
    return {"role": "unknown"}


def item_to_dict(item: DecodedItem, preview_only: bool = False) -> Dict[str, Any]:
    payload = {
        "kind": item.kind,
        "role": item.role,
        "raw_key": item.raw_key,
        "logical_key": item.logical_key,
        "storage_command": item.storage_command,
        "scope": item.scope,
        "expiry_at_ms": item.expiry_at_ms,
        "expiry_at": item.expiry_at,
    }
    for key, value in item.data.items():
        if preview_only:
            if key in {"answer", "question", "raw_question", "canonical_question", "target_question", "target_answer", "canonical_answer", "value_preview"}:
                payload[key] = preview_text(value, 96)
            elif key in {"vector_preview", "member_sample", "member_targets_sample", "exact_index_keys", "normalized_index_keys", "alias_index_keys", "candidate_bucket_keys", "semantic_scope_keys"}:
                payload[key] = value
            elif key == "payload_summary" and isinstance(value, dict):
                payload[key] = value
            elif key == "semantic_payload" and isinstance(value, dict):
                payload[key] = {
                    "model": value.get("model", ""),
                    "provider": value.get("provider", ""),
                    "dim": value.get("dim", 0),
                    "vector_length": value.get("vector_length", 0),
                    "vector_preview": value.get("vector_preview", []),
                }
            else:
                payload[key] = value
        else:
            payload[key] = value
    return payload


def item_kind(item: Any) -> str:
    if isinstance(item, DecodedItem):
        return item.kind
    if isinstance(item, dict):
        return to_text(item.get("kind")).strip()
    return ""


def item_role(item: Any) -> str:
    if isinstance(item, DecodedItem):
        return item.role
    if isinstance(item, dict):
        return to_text(item.get("role")).strip()
    return ""


def item_raw_key(item: Any) -> str:
    if isinstance(item, DecodedItem):
        return item.raw_key
    if isinstance(item, dict):
        return to_text(item.get("raw_key")).strip()
    return ""


def item_logical_key(item: Any) -> str:
    if isinstance(item, DecodedItem):
        return item.logical_key
    if isinstance(item, dict):
        return to_text(item.get("logical_key")).strip()
    return ""


def item_scope(item: Any) -> str:
    if isinstance(item, DecodedItem):
        return item.scope
    if isinstance(item, dict):
        return to_text(item.get("scope")).strip()
    return ""


def item_expiry_at(item: Any) -> str:
    if isinstance(item, DecodedItem):
        return item.expiry_at
    if isinstance(item, dict):
        return to_text(item.get("expiry_at")).strip()
    return ""


def item_data(item: Any) -> Dict[str, Any]:
    if isinstance(item, DecodedItem):
        return item.data
    if isinstance(item, dict):
        return item
    return {}


def count_by_kind(items: List[DecodedItem]) -> Dict[str, int]:
    counter = Counter(item.kind for item in items)
    result: Dict[str, int] = {}
    for kind in ["main_record", "exact_index", "normalized_index", "alias_index", "candidate_index", "semantic_embedding", "semantic_scope_index", "unknown"]:
        result[kind] = counter.get(kind, 0)
    return result


def count_by_command(items: List[DecodedItem]) -> Dict[str, int]:
    counter = Counter(item.storage_command for item in items)
    return dict(sorted(counter.items(), key=lambda pair: pair[0]))


def count_scopes(items: List[DecodedItem]) -> Dict[str, int]:
    counter = Counter()
    for item in items:
        if item.kind == "main_record":
            scope = to_text(item.data.get("scope")).strip()
        else:
            scope = item.scope or to_text(item.data.get("scope")).strip()
        if scope:
            counter[scope] += 1
    return dict(sorted(counter.items(), key=lambda pair: (-pair[1], pair[0])))


def build_summary(items: List[DecodedItem], filtered_items: List[DecodedItem]) -> Dict[str, Any]:
    kind_counts = count_by_kind(items)
    visible_kind_counts = count_by_kind(filtered_items)
    summary = {
        "total_items": len(items),
        "visible_items": len(filtered_items),
        "kind_counts": kind_counts,
        "visible_kind_counts": visible_kind_counts,
        "storage_command_counts": count_by_command(items),
        "scopes": count_scopes(items),
        "main_record_count": kind_counts.get("main_record", 0),
        "semantic_record_count": kind_counts.get("semantic_embedding", 0) + kind_counts.get("semantic_scope_index", 0),
        "ann_persisted": False,
        "notes": [
            "ANN 索引没有持久化 key；当前代码使用的是 KVLiteStore 内存中的 semanticANNCache。",
            "prompt_tokens / completion_tokens / total_tokens 不在 QARecord 顶层字段里，当前工具只会在真实数据存在时展示。",
            "answer_source_kind / qa_hit_type / semantic_mode_used / semantic_fallback_* 属于运行时 response meta，持久化记录里通常为空。",
        ],
    }
    return summary


def make_kind_filter(args: argparse.Namespace) -> List[str]:
    kinds: List[str] = []
    if args.kind:
        kinds.extend(args.kind)
    if args.main_only:
        kinds.append("main")
    if args.index_only:
        kinds.append("index")
    if args.semantic_only:
        kinds.append("semantic")
    if args.unknown_only:
        kinds.append("unknown")
    return apply_kind_aliases(kinds)


def render_markdown_table(headers: Sequence[str], rows: Sequence[Sequence[Any]]) -> str:
    lines = []
    header_cells = [escape_cell(header) for header in headers]
    lines.append("| " + " | ".join(header_cells) + " |")
    lines.append("| " + " | ".join(["---"] * len(headers)) + " |")
    for row in rows:
        cells = [escape_cell(cell) for cell in row]
        lines.append("| " + " | ".join(cells) + " |")
    return "\n".join(lines)


def render_ascii_table(headers: Sequence[str], rows: Sequence[Sequence[Any]]) -> str:
    str_rows = [[to_text(cell).replace("\n", " ").replace("\r", " ") for cell in row] for row in rows]
    widths = [len(to_text(header)) for header in headers]
    for row in str_rows:
        for index, cell in enumerate(row):
            widths[index] = max(widths[index], len(cell))
    lines = []
    header_line = " | ".join(to_text(header).ljust(widths[index]) for index, header in enumerate(headers))
    separator = "-+-".join("-" * widths[index] for index in range(len(headers)))
    lines.append(header_line)
    lines.append(separator)
    for row in str_rows:
        lines.append(" | ".join(row[index].ljust(widths[index]) for index in range(len(headers))))
    return "\n".join(lines)


def render_prefix_catalog(output: str) -> str:
    rows = []
    for item in KNOWN_PREFIXES:
        rows.append([
            item["label"],
            item["kind"],
            item["prefix"] or "(none)",
            item["value"],
            item["role"],
        ])
    headers = ["label", "kind", "prefix", "stored value", "role"]
    if output == "json":
        return json.dumps(KNOWN_PREFIXES, ensure_ascii=False, indent=2)
    if output == "table":
        return render_ascii_table(headers, rows)
    return render_markdown_table(headers, rows)


def render_summary(summary: Dict[str, Any], output: str) -> str:
    rows = [
        ["total_items", summary["total_items"]],
        ["visible_items", summary["visible_items"]],
        ["main_record_count", summary["main_record_count"]],
        ["semantic_record_count", summary["semantic_record_count"]],
        ["ann_persisted", summary["ann_persisted"]],
    ]
    headers = ["metric", "value"]
    if output == "table":
        return render_ascii_table(headers, rows)
    return render_markdown_table(headers, rows)


def render_counts_table(counts: Dict[str, int], output: str) -> str:
    rows = [[kind, count] for kind, count in counts.items()]
    headers = ["kind", "count"]
    if output == "table":
        return render_ascii_table(headers, rows)
    return render_markdown_table(headers, rows)


def render_scope_table(scopes: Dict[str, int], output: str) -> str:
    rows = [[scope_display(scope), count] for scope, count in scopes.items()]
    headers = ["scope", "count"]
    if output == "table":
        return render_ascii_table(headers, rows)
    return render_markdown_table(headers, rows)


def render_main_records(items: List[DecodedItem], output: str) -> str:
    rows = []
    for item in items:
        data = item_data(item)
        token_summary = ""
        prompt_tokens = data.get("prompt_tokens")
        completion_tokens = data.get("completion_tokens")
        total_tokens = data.get("total_tokens")
        if any(value is not None for value in (prompt_tokens, completion_tokens, total_tokens)):
            token_summary = f"{prompt_tokens or 0}/{completion_tokens or 0}/{total_tokens or 0}"
        else:
            token_summary = "-"
        semantic_flag = "yes" if data.get("has_semantic_vector") else "no"
        relation_flags = []
        for label in ("exact", "normalized", "alias", "candidate", "semantic_scope"):
            if data.get(f"has_{label}_index"):
                relation_flags.append(label)
        rows.append([
            preview_text(data.get("question_id"), 48),
            preview_text(data.get("question"), 72),
            preview_text(data.get("answer"), 96),
            preview_text(data.get("canonical_question"), 72),
            preview_text(data.get("provider"), 18),
            preview_text(data.get("model"), 22),
            preview_text(data.get("prompt_version"), 12),
            preview_text(data.get("scope"), 42),
            preview_text(data.get("source_type"), 12),
            preview_text(data.get("answer_source"), 18),
            token_summary,
            semantic_flag,
            ", ".join(relation_flags) if relation_flags else "-",
            preview_text(data.get("created_at_human"), 24),
        ])
    headers = [
        "question_id",
        "question",
        "answer",
        "canonical_question",
        "provider",
        "model",
        "prompt_version",
        "scope",
        "source_type",
        "answer_source",
        "tokens(p/c/t)",
        "semantic",
        "coverage",
        "created_at",
    ]
    if output == "table":
        return render_ascii_table(headers, rows)
    return render_markdown_table(headers, rows)


def render_index_group(items: List[DecodedItem], output: str, group_kind: str) -> str:
    rows = []
    for item in items:
        data = item_data(item)
        kind = item_kind(item)
        if kind in {"exact_index", "normalized_index"}:
            rows.append([
                preview_text(item_raw_key(item), 64),
                preview_text(data.get("scope"), 40),
                preview_text(data.get("hash"), 20),
                preview_text(data.get("target_question_id"), 52),
                preview_text(data.get("target_question"), 68),
                preview_text(data.get("target_answer"), 72),
                "yes" if data.get("target_exists") else "no",
                preview_text(item_expiry_at(item), 24),
            ])
        elif kind == "alias_index":
            rows.append([
                preview_text(item_raw_key(item), 64),
                preview_text(data.get("scope"), 40),
                preview_text(data.get("alias_hash"), 20),
                preview_text(data.get("canonical_question_id"), 52),
                preview_text(data.get("canonical_question"), 68),
                preview_text(data.get("canonical_answer"), 72),
                "yes" if data.get("target_exists") else "no",
                preview_text(item_expiry_at(item), 24),
            ])
        elif kind == "candidate_index":
            rows.append([
                preview_text(item_raw_key(item), 72),
                preview_text(data.get("scope"), 40),
                preview_text(data.get("signal_text"), 24),
                data.get("member_count", 0),
                ", ".join([preview_text(x, 18) for x in data.get("member_sample", [])]) or "-",
                data.get("known_target_count", 0),
                preview_text(item_expiry_at(item), 24),
            ])
        elif kind == "semantic_embedding":
            rows.append([
                preview_text(item_raw_key(item), 72),
                preview_text(data.get("question_id"), 52),
                preview_text(data.get("provider"), 18),
                preview_text(data.get("model"), 22),
                data.get("dim") or 0,
                data.get("vector_length") or 0,
                ", ".join([preview_text(x, 18) for x in data.get("vector_preview", [])]) or "-",
                "yes" if data.get("ready") else "no",
                preview_text(data.get("target_question"), 64),
                preview_text(item_expiry_at(item), 24),
            ])
        elif kind == "semantic_scope_index":
            rows.append([
                preview_text(item_raw_key(item), 72),
                preview_text(data.get("scope"), 40),
                data.get("member_count", 0),
                ", ".join([preview_text(x, 18) for x in data.get("member_sample", [])]) or "-",
                data.get("known_target_count", 0),
                preview_text(item_expiry_at(item), 24),
            ])
        else:
            rows.append([
                preview_text(item_raw_key(item), 72),
                preview_text(item_logical_key(item), 64),
                preview_text(data.get("value_preview"), 96),
                preview_text(data.get("json_error"), 32),
                preview_text(item_expiry_at(item), 24),
            ])
    if not rows:
        return ""
    sample_kind = item_kind(items[0]) if items else group_kind
    if sample_kind in {"exact_index", "normalized_index"}:
        headers = ["key", "scope", "hash", "target_question_id", "target_question", "target_answer", "target_exists", "expires_at"]
    elif sample_kind == "alias_index":
        headers = ["key", "scope", "alias_hash", "canonical_question_id", "canonical_question", "canonical_answer", "target_exists", "expires_at"]
    elif sample_kind == "candidate_index":
        headers = ["key", "scope", "signal", "bucket_size", "sample_question_ids", "known_targets", "expires_at"]
    elif sample_kind == "semantic_embedding":
        headers = ["key", "question_id", "provider", "model", "dim", "vector_len", "vector_preview", "ready", "target_question", "expires_at"]
    elif sample_kind == "semantic_scope_index":
        headers = ["key", "scope", "member_count", "sample_question_ids", "known_targets", "expires_at"]
    else:
        headers = ["key", "logical_key", "value_preview", "json_error", "expires_at"]
    if output == "table":
        return render_ascii_table(headers, rows)
    return render_markdown_table(headers, rows)


def render_unknown_items(items: List[DecodedItem], output: str) -> str:
    rows = []
    for item in items:
        data = item_data(item)
        rows.append([
            preview_text(item_raw_key(item), 72),
            preview_text(item_logical_key(item), 64),
            preview_text(item.get("storage_command") if isinstance(item, dict) else item.storage_command, 10),
            preview_text(data.get("value_preview"), 96),
            preview_text(data.get("json_error"), 40),
            preview_text(item_expiry_at(item), 24),
        ])
    headers = ["key", "logical_key", "cmd", "value_preview", "json_error", "expires_at"]
    if output == "table":
        return render_ascii_table(headers, rows)
    return render_markdown_table(headers, rows)


def render_target_explanation(explanation: Dict[str, Any], output: str) -> str:
    if explanation.get("status") == "not_found":
        rows = [
            ["status", explanation.get("status")],
            ["question_id", explanation.get("question_id")],
            ["message", explanation.get("message")],
        ]
        if output == "table":
            parts = [render_ascii_table(["field", "value"], rows)]
        else:
            parts = [render_markdown_table(["field", "value"], rows)]
        related_entries = explanation.get("related_entries", [])
        if related_entries:
            related_rows = []
            for item in related_entries:
                related_rows.append([
                    preview_text(item.get("raw_key"), 72),
                    preview_text(item.get("role"), 48),
                    preview_text(item.get("logical_key"), 64),
                    preview_text(item.get("target_question_id") or item.get("canonical_question_id") or item.get("question_id"), 52),
                    preview_text(item.get("target_question") or item.get("canonical_question") or item.get("question"), 72),
                ])
            headers = ["raw_key", "role", "logical_key", "target_id", "target_question"]
            if output == "table":
                parts.append("### related_entries")
                parts.append(render_ascii_table(headers, related_rows))
            else:
                parts.append("### related_entries")
                parts.append(render_markdown_table(headers, related_rows))
        return "\n\n".join(parts)
    payload = explanation.get("payload", {})
    rows = []
    ordered_keys = [
        "question_id",
        "record_id",
        "canonical_question_id",
        "question",
        "raw_question",
        "canonical_question",
        "normalized_question",
        "answer",
        "scope",
        "prompt_version",
        "provider",
        "model",
        "source_type",
        "answer_source",
        "hit_count",
        "last_hit_at",
        "created_at",
        "updated_at",
        "prompt_tokens",
        "completion_tokens",
        "total_tokens",
        "answer_source_kind",
        "qa_hit_type",
        "semantic_mode_used",
        "semantic_fallback",
        "semantic_fallback_reason",
        "embedding_status",
        "embedding_model",
        "embedding_dim",
        "embedding_updated_at",
        "embedding_ref",
        "semantic_ready",
        "has_exact_index",
        "has_normalized_index",
        "has_alias_index",
        "has_candidate_index",
        "has_semantic_scope_index",
        "has_semantic_vector",
    ]
    for key in ordered_keys:
        rows.append([key, preview_text(payload.get(key), 120)])
    if output == "table":
        explanation_table = render_ascii_table(["field", "value"], rows)
    else:
        explanation_table = render_markdown_table(["field", "value"], rows)
    relation_rows = []
    relations = explanation.get("relations", {})
    for key, value in relations.items():
        relation_rows.append([key, preview_text(value, 160)])
    if output == "table":
        relation_table = render_ascii_table(["relation", "value"], relation_rows) if relation_rows else ""
    else:
        relation_table = render_markdown_table(["relation", "value"], relation_rows) if relation_rows else ""
    linked_indexes = explanation.get("linked_indexes")
    linked_tables = []
    if isinstance(linked_indexes, dict):
        for group, group_items in linked_indexes.items():
            if not group_items:
                continue
            rows = []
            for item in group_items:
                rows.append([
                    preview_text(item.get("raw_key"), 72),
                    preview_text(item.get("role"), 48),
                    preview_text(item.get("logical_key"), 64),
                    preview_text(item.get("target_question_id") or item.get("canonical_question_id") or item.get("question_id"), 52),
                    preview_text(item.get("target_question") or item.get("canonical_question") or item.get("question"), 72),
                ])
            headers = ["raw_key", "role", "logical_key", "target_id", "target_question"]
            if output == "table":
                linked_tables.append(f"### {group}\n{render_ascii_table(headers, rows)}")
            else:
                linked_tables.append(f"### {group}\n{render_markdown_table(headers, rows)}")
    parts = ["## record_explanation", explanation_table]
    if relation_table:
        parts.extend(["### relations", relation_table])
    if linked_tables:
        parts.extend(linked_tables)
    return "\n\n".join(parts)


def render_report(report: Dict[str, Any], output: str) -> str:
    if output == "json":
        return json.dumps(report, ensure_ascii=False, indent=2)
    parts = []
    parts.append("# KVLite Q&A Decode Report")
    source = report.get("source", {})
    source_rows = [
        ["mode", source.get("mode", "")],
        ["addr", source.get("addr", "")],
        ["namespace", source.get("namespace", "")],
        ["input_path", source.get("input_path", "")],
        ["raw_bytes", source.get("raw_bytes", 0)],
        ["raw_entries", source.get("raw_entries", 0)],
    ]
    if output == "table":
        parts.append("## source")
        parts.append(render_ascii_table(["field", "value"], source_rows))
        parts.append("## known_prefixes")
        parts.append(render_prefix_catalog(output))
        parts.append("## summary")
        parts.append(render_summary(report["summary"], output))
        parts.append("## counts_by_kind")
        parts.append(render_counts_table(report["summary"]["kind_counts"], output))
        parts.append("## visible_counts_by_kind")
        parts.append(render_counts_table(report["summary"]["visible_kind_counts"], output))
        if report["summary"].get("scopes"):
            parts.append("## scopes")
            parts.append(render_scope_table(report["summary"]["scopes"], output))
    else:
        parts.append("## source")
        parts.append(render_markdown_table(["field", "value"], source_rows))
        parts.append("## known_prefixes")
        parts.append(render_prefix_catalog(output))
        parts.append("## summary")
        parts.append(render_summary(report["summary"], output))
        parts.append("## counts_by_kind")
        parts.append(render_counts_table(report["summary"]["kind_counts"], output))
        parts.append("## visible_counts_by_kind")
        parts.append(render_counts_table(report["summary"]["visible_kind_counts"], output))
        if report["summary"].get("scopes"):
            parts.append("## scopes")
            parts.append(render_scope_table(report["summary"]["scopes"], output))

    groups = [
        ("main_records", "## question_list_view", render_main_records),
        ("exact_indexes", "## exact_indexes", render_index_group),
        ("normalized_indexes", "## normalized_indexes", render_index_group),
        ("alias_indexes", "## alias_indexes", render_index_group),
        ("candidate_indexes", "## candidate_indexes", render_index_group),
        ("semantic_embeddings", "## semantic_embeddings", render_index_group),
        ("semantic_scope_indexes", "## semantic_scope_indexes", render_index_group),
        ("unknown_records", "## unknown_records", render_unknown_items),
    ]
    for group_key, title, renderer in groups:
        group_items = report.get(group_key, [])
        if not group_items:
            continue
        parts.append(title)
        if renderer is render_index_group:
            group_kind = group_key[:-1] if group_key.endswith("s") else group_key
            rendered = renderer(group_items, output, group_kind)
        else:
            rendered = renderer(group_items, output)
        if rendered:
            parts.append(rendered)

    explanation = report.get("explanation")
    if explanation:
        if output == "table":
            parts.append(render_target_explanation(explanation, output))
        else:
            parts.append(render_target_explanation(explanation, output))
    return "\n\n".join(parts)


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        description="Decode KVLite QA cache into business-field views.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--source", choices=["direct", "file"], default="direct", help="Fetch mode. direct connects to KVLite; file parses raw FULLDUMP RESP bytes.")
    parser.add_argument("--input", help="Raw FULLDUMP RESP file path when --source=file.")
    parser.add_argument("--save-raw", help="When using --source=direct, save the raw FULLDUMP RESP bytes to this file.")
    parser.add_argument("--addr", default=DEFAULT_ADDR, help="KVLite host:port, default matches ai-chat dev.config.yaml.")
    parser.add_argument("--namespace", default=DEFAULT_NAMESPACE, help="QA namespace. You can pass ai:chat or ai:chat:qa; it will be normalized to the actual KVLite namespace.")
    parser.add_argument("--timeout", type=float, default=5.0, help="Socket timeout in seconds for direct mode.")
    parser.add_argument("--output", choices=["markdown", "table", "json"], default="markdown", help="Output format.")
    parser.add_argument("--kind", action="append", default=[], help="Filter by kind: main, exact, normalized, alias, candidate, semantic, unknown, all, index.")
    parser.add_argument("--main-only", action="store_true", help="Show only main records.")
    parser.add_argument("--index-only", action="store_true", help="Show only index records.")
    parser.add_argument("--semantic-only", action="store_true", help="Show only semantic-related records.")
    parser.add_argument("--unknown-only", action="store_true", help="Show only unknown keys.")
    parser.add_argument("--question-id", help="Explain one main record by question_id.")
    parser.add_argument("--key", help="Explain one raw key or logical key directly.")
    parser.add_argument("--scope", help="Filter by scope.")
    parser.add_argument("--max-preview-chars", type=int, default=DEFAULT_PREVIEW_CHARS, help="Reserved for future tuning; previews are already bounded.")
    args = parser.parse_args(argv)

    namespace = normalize_namespace(args.namespace)
    raw_bytes = load_raw_dump_bytes(args.source, args.input, args.addr, args.timeout, args.save_raw)
    try:
        parsed_frames = parse_raw_responses(raw_bytes)
    except RespParseError as exc:
        print(f"failed to parse RESP dump: {exc}", file=sys.stderr)
        return 2

    raw_entries: List[RawDumpEntry] = []
    for frame in parsed_frames:
        entry = decode_command_frame(frame)
        if entry is not None:
            raw_entries.append(entry)

    items, _expiry_map = build_decoded_items(raw_entries, namespace)
    maps = build_lookup_maps(items)
    enrich_items(items, maps)

    selected_kinds = make_kind_filter(args)
    filters = {
        "question_id": (args.question_id or "").strip(),
        "key": (args.key or "").strip(),
        "scope": (args.scope or "").strip(),
        "semantic_only": bool(args.semantic_only),
        "unknown_only": bool(args.unknown_only),
        "selected_kinds": set(selected_kinds) if selected_kinds else set(),
    }

    visible_items = [item for item in items if item_matches_filters(item, filters)]
    summary = build_summary(items, visible_items)

    def filter_group(group_kind: str) -> List[DecodedItem]:
        if not selected_kinds and not args.main_only and not args.index_only and not args.semantic_only and not args.unknown_only and not args.question_id and not args.key and not args.scope:
            return [item for item in items if item.kind == group_kind]
        return [item for item in visible_items if item.kind == group_kind]

    report: Dict[str, Any] = {
        "source": {
            "mode": args.source,
            "addr": args.addr,
            "namespace": namespace,
            "input_path": args.input or "",
            "save_raw": args.save_raw or "",
            "raw_bytes": len(raw_bytes),
            "raw_entries": len(raw_entries),
        },
        "known_prefixes": KNOWN_PREFIXES,
        "summary": summary,
        "main_records": [item_to_dict(item, preview_only=False) for item in filter_group("main_record")],
        "exact_indexes": [item_to_dict(item, preview_only=False) for item in filter_group("exact_index")],
        "normalized_indexes": [item_to_dict(item, preview_only=False) for item in filter_group("normalized_index")],
        "alias_indexes": [item_to_dict(item, preview_only=False) for item in filter_group("alias_index")],
        "candidate_indexes": [item_to_dict(item, preview_only=False) for item in filter_group("candidate_index")],
        "semantic_embeddings": [item_to_dict(item, preview_only=False) for item in filter_group("semantic_embedding")],
        "semantic_scope_indexes": [item_to_dict(item, preview_only=False) for item in filter_group("semantic_scope_index")],
        "unknown_records": [item_to_dict(item, preview_only=False) for item in filter_group("unknown")],
        "explanation": None,
    }

    target_item = None
    target_question_id = (args.question_id or "").strip()
    if args.key:
        target_key = (args.key or "").strip()
        for item in items:
            if item.raw_key == target_key or item.logical_key == target_key:
                target_item = item
                break
    elif target_question_id:
        for item in items:
            if item.kind == "main_record" and to_text(item.data.get("question_id")).strip() == target_question_id:
                target_item = item
                break
    if target_item or target_question_id:
        report["explanation"] = build_target_explanation(target_item, items, maps, question_id=target_question_id)

    output_text = render_report(report, args.output)
    print(output_text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())