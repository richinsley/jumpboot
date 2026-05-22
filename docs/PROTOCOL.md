# Jumpboot Queue Protocol

This document specifies the wire protocol spoken between the Go host
(`QueueProcess`, `pyprocqueue.go`) and a language runtime's queue server (the
embedded `MessagePackQueueServer`, `packages/jumpboot/msgpackqueue.py`).

It is the **portable contract**. `QueueProcess` itself is language-neutral —
any runtime that implements this protocol on its side can be driven by the
same Go code. Five runtimes implement it today:

| Runtime    | SDK                      | Transport              |
|------------|--------------------------|------------------------|
| Python     | `packages/jumpboot/`     | OS pipes (subprocess)  |
| Node.js    | `packages/jumpboot-js/`  | OS pipes (subprocess)  |
| Deno       | `packages/jumpboot-deno/`| stdio (subprocess)     |
| Julia      | `packages/jumpboot-julia/`| stdio (subprocess)    |
| WebAssembly| `wasmguest/`             | virtual pipes (wazero, in-process) |

Each runtime SDK is a conformance target for this spec; see
`protocol_conformance_test.go`, which runs an identical assertion table
against every runtime.

## 1. Transport framing

Messages travel over a pair of byte streams (OS pipes for subprocess runtimes,
virtual pipes for in-process runtimes). Each message is **length-prefixed**:

```
+----------------------+--------------------------------+
| length (4 bytes, BE) | MessagePack payload (length B) |
+----------------------+--------------------------------+
```

- The length is a 4-byte **big-endian** `uint32` — the byte count of the
  payload that follows.
- The payload is a single **MessagePack-encoded map**.
- The sender flushes after the length prefix and again after the payload, so a
  reader that has the 4 length bytes can always read exactly that many payload
  bytes (loop on short reads — a pipe `read` may return fewer bytes than asked).

Reference implementations: `MsgpackTransport` (`ipcmessagepack.go`) and
`MessagePackTransport` (`packages/jumpboot/msgpackqueue.py`).

## 2. Message shapes

Every message is a map. The direction is symmetric — both sides may initiate
requests and both send responses.

### Request

```
{
  "command":    <string>,   # method name, or a built-in (see §4)
  "data":       <any>,      # arguments: a map (kwargs) or array (positional)
  "request_id": <string>,   # unique id, see §3
  "stream":     <bool>      # optional; true requests streaming (see §5)
}
```

### Response

A response echoes the originating `request_id` and carries exactly one of
`result` or `error`:

```
{ "request_id": <string>, "result": <any> }
{ "request_id": <string>, "error": <string>, "traceback": <string> }   # traceback optional
```

A response with neither `result` nor `error`, or with extra keys, is unwrapped
leniently by the Go side: after dropping `request_id`, a single remaining field
is returned as the result (`extractCallResult`, `pyprocqueue.go`).

## 3. Request-ID namespacing

Each side prefixes the IDs it generates so the peer can tell a *response to my
request* apart from a *new request from the peer*:

| Originator        | Prefix  | Example  |
|-------------------|---------|----------|
| Go host           | `req-`  | `req-1`  |
| Runtime queue srv | `py-`   | `py-1`   |

Routing rule on the Go side (`messageLoop`): an incoming frame whose
`request_id` does **not** start with `py-` is a response to a Go-initiated
request; otherwise it is a request *from* the runtime. The runtime side applies
the mirror rule (`py-` prefix = its own).

> **SDK note:** the `py-` prefix is historical (Python was the first runtime).
> It does **not** mean "Python" — it means "originated by the runtime side".
> A Node.js or any future SDK that initiates requests back to Go **must** also
> use the `py-` prefix so the Go router classifies them correctly.

## 4. Built-in commands

Every queue server must implement these reserved command names:

| Command          | Data                          | Response                                            |
|------------------|-------------------------------|-----------------------------------------------------|
| `__get_methods__`| none                          | `{ "methods": { <name>: <MethodInfo> } }` (see §6)  |
| `__cancel__`     | `{ "target_request_id": id }` | `{ "ok": bool, "target_request_id"?, "error"? }`    |
| `exit`           | none                          | none — process terminates immediately               |
| `shutdown`       | none                          | `{ "status": "shutting_down" }`, then graceful stop  |

`exit` is fire-and-forget (the Go side does not wait for a reply). `shutdown`
replies, then stops the server loop cleanly.

## 5. Streaming and cancellation

**Streaming.** A request with `"stream": true` permits the handler to emit
intermediate frames that share the request's `request_id`:

- A partial frame carries `"done": false` and keeps the request open.
- The terminal frame carries `"done": true` (or omits `done` entirely —
  a missing `done` means terminal, preserving legacy single-reply behaviour).
- An error frame on a streaming request must also set `"done": true`.

The Go side (`CallStream`) yields every frame on a channel and closes it once a
terminal frame arrives.

**Cancellation.** Cancellation is cooperative. The Go side sends `__cancel__`
with the in-flight `target_request_id`; the queue server sets a cancel event
for that id. A handler that polls its cancel event can abort early (typically
returning a "cancelled" response); a handler that never polls runs to
completion. After sending `__cancel__`, the Go side waits a 5-second grace
period for the handler to surface, then returns `ctx.Err()` regardless.

## 6. Method discovery (`MethodInfo`)

`__get_methods__` returns a map of exposed method names to metadata:

```
MethodInfo  = { "parameters": [ParameterInfo], "return": { "type"?: string }, "doc": string }
ParameterInfo = { "name": string, "required": bool, "type"?: string }
```

`required` is true when the parameter has no default value. `type` carries the
runtime's type annotation when one is available. The Go side caches this and
exposes it via `QueueProcess.GetMethods()` / `GetMethodInfo()`.

## 7. Conformance

A runtime SDK is protocol-conformant when, driven by an unmodified
`QueueProcess`, it satisfies the assertions in `protocol_conformance_test.go`:
basic call, multi-argument call, error propagation, and method discovery (with
streaming and cancellation covered by `pyprocqueue_stream_test.go` /
`pyprocqueue_cancel_test.go` and their per-runtime analogs). The conformance
harness is invoked once per runtime so every SDK is held to the identical bar.
