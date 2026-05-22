# jumpboot — Julia runtime SDK.
#
# This single self-contained file is concatenated ahead of a user plugin and
# run with `julia -e`. The plugin calls register(...) to expose handlers and
# then serve(); the server speaks the jumpboot queue protocol (docs/PROTOCOL.md)
# over stdin/stdout, so an unmodified Go QueueProcess can drive it.
#
# It is dependency-free — it bundles a minimal MessagePack codec — so a plugin
# needs no Julia packages installed.
#
# Concurrency note: serve() processes one request at a time. Calls and
# streaming (handlers may call emit) work; mid-handler cancellation is not
# observable — cancelled(ctx) always returns false. A plugin must not write to
# stdout: it is the protocol channel. Use stderr for diagnostics.

# ===== MessagePack codec ===================================================

function mp_put_uint!(out::Vector{UInt8}, val::UInt64, nbytes::Int)
    for i in (nbytes - 1):-1:0
        push!(out, UInt8((val >> (8 * i)) & 0xff))
    end
end

function mp_encode_int!(out::Vector{UInt8}, n::Int64)
    if n >= 0
        u = UInt64(n)
        if n < 128
            push!(out, UInt8(u))
        elseif n < 256
            push!(out, 0xcc, UInt8(u))
        elseif n < 65536
            push!(out, 0xcd)
            mp_put_uint!(out, u, 2)
        elseif n < 4294967296
            push!(out, 0xce)
            mp_put_uint!(out, u, 4)
        else
            push!(out, 0xcf)
            mp_put_uint!(out, u, 8)
        end
    else
        if n >= -32
            push!(out, reinterpret(UInt8, Int8(n)))
        elseif n >= -128
            push!(out, 0xd0, reinterpret(UInt8, Int8(n)))
        elseif n >= -32768
            push!(out, 0xd1)
            mp_put_uint!(out, UInt64(reinterpret(UInt16, Int16(n))), 2)
        elseif n >= -2147483648
            push!(out, 0xd2)
            mp_put_uint!(out, UInt64(reinterpret(UInt32, Int32(n))), 4)
        else
            push!(out, 0xd3)
            mp_put_uint!(out, reinterpret(UInt64, n), 8)
        end
    end
end

function mp_encode_str!(out::Vector{UInt8}, s::String)
    data = Vector{UInt8}(s)
    n = length(data)
    if n < 32
        push!(out, 0xa0 | UInt8(n))
    elseif n < 256
        push!(out, 0xd9, UInt8(n))
    elseif n < 65536
        push!(out, 0xda)
        mp_put_uint!(out, UInt64(n), 2)
    else
        push!(out, 0xdb)
        mp_put_uint!(out, UInt64(n), 4)
    end
    append!(out, data)
end

function mp_encode_bin!(out::Vector{UInt8}, data::Vector{UInt8})
    n = length(data)
    if n < 256
        push!(out, 0xc4, UInt8(n))
    elseif n < 65536
        push!(out, 0xc5)
        mp_put_uint!(out, UInt64(n), 2)
    else
        push!(out, 0xc6)
        mp_put_uint!(out, UInt64(n), 4)
    end
    append!(out, data)
end

function mp_encode_array!(out::Vector{UInt8}, arr)
    n = length(arr)
    if n < 16
        push!(out, 0x90 | UInt8(n))
    elseif n < 65536
        push!(out, 0xdc)
        mp_put_uint!(out, UInt64(n), 2)
    else
        push!(out, 0xdd)
        mp_put_uint!(out, UInt64(n), 4)
    end
    for item in arr
        mp_encode_value!(out, item)
    end
end

function mp_encode_map!(out::Vector{UInt8}, m)
    n = length(m)
    if n < 16
        push!(out, 0x80 | UInt8(n))
    elseif n < 65536
        push!(out, 0xde)
        mp_put_uint!(out, UInt64(n), 2)
    else
        push!(out, 0xdf)
        mp_put_uint!(out, UInt64(n), 4)
    end
    for (k, v) in m
        mp_encode_str!(out, string(k))
        mp_encode_value!(out, v)
    end
end

function mp_encode_value!(out::Vector{UInt8}, v)
    if v === nothing
        push!(out, 0xc0)
    elseif v isa Bool
        push!(out, v ? 0xc3 : 0xc2)
    elseif v isa Integer
        mp_encode_int!(out, Int64(v))
    elseif v isa AbstractFloat
        push!(out, 0xcb)
        mp_put_uint!(out, reinterpret(UInt64, Float64(v)), 8)
    elseif v isa AbstractString
        mp_encode_str!(out, String(v))
    elseif v isa Vector{UInt8}
        mp_encode_bin!(out, v)
    elseif v isa AbstractVector
        mp_encode_array!(out, v)
    elseif v isa AbstractDict
        mp_encode_map!(out, v)
    else
        mp_encode_str!(out, string(v))
    end
end

function mp_encode(v)::Vector{UInt8}
    out = UInt8[]
    mp_encode_value!(out, v)
    return out
end

mutable struct MpReader
    buf::Vector{UInt8}
    pos::Int
end

function mp_read_byte!(r::MpReader)::UInt8
    b = r.buf[r.pos]
    r.pos += 1
    return b
end

function mp_read_uint!(r::MpReader, n::Int)::UInt64
    v = UInt64(0)
    for _ in 1:n
        v = (v << 8) | UInt64(r.buf[r.pos])
        r.pos += 1
    end
    return v
end

function mp_take!(r::MpReader, n::Int)::Vector{UInt8}
    s = r.buf[r.pos:(r.pos + n - 1)]
    r.pos += n
    return s
end

function mp_decode_array!(r::MpReader, n::Int)
    arr = Vector{Any}(undef, n)
    for i in 1:n
        arr[i] = mp_decode_value!(r)
    end
    return arr
end

function mp_decode_map!(r::MpReader, n::Int)
    m = Dict{String,Any}()
    for _ in 1:n
        k = mp_decode_value!(r)
        v = mp_decode_value!(r)
        m[string(k)] = v
    end
    return m
end

function mp_decode_value!(r::MpReader)
    b = mp_read_byte!(r)
    if b <= 0x7f
        return Int64(b)
    elseif b >= 0xe0
        return Int64(reinterpret(Int8, b))
    elseif 0x80 <= b <= 0x8f
        return mp_decode_map!(r, Int(b & 0x0f))
    elseif 0x90 <= b <= 0x9f
        return mp_decode_array!(r, Int(b & 0x0f))
    elseif 0xa0 <= b <= 0xbf
        return String(mp_take!(r, Int(b & 0x1f)))
    end
    if b == 0xc0
        return nothing
    elseif b == 0xc2
        return false
    elseif b == 0xc3
        return true
    elseif b == 0xc4
        return mp_take!(r, Int(mp_read_byte!(r)))
    elseif b == 0xc5
        return mp_take!(r, Int(mp_read_uint!(r, 2)))
    elseif b == 0xc6
        return mp_take!(r, Int(mp_read_uint!(r, 4)))
    elseif b == 0xca
        return Float64(reinterpret(Float32, UInt32(mp_read_uint!(r, 4))))
    elseif b == 0xcb
        return reinterpret(Float64, mp_read_uint!(r, 8))
    elseif b == 0xcc
        return Int64(mp_read_byte!(r))
    elseif b == 0xcd
        return Int64(mp_read_uint!(r, 2))
    elseif b == 0xce
        return Int64(mp_read_uint!(r, 4))
    elseif b == 0xcf
        u = mp_read_uint!(r, 8)
        return u <= UInt64(typemax(Int64)) ? Int64(u) : u
    elseif b == 0xd0
        return Int64(reinterpret(Int8, mp_read_byte!(r)))
    elseif b == 0xd1
        return Int64(reinterpret(Int16, UInt16(mp_read_uint!(r, 2))))
    elseif b == 0xd2
        return Int64(reinterpret(Int32, UInt32(mp_read_uint!(r, 4))))
    elseif b == 0xd3
        return Int64(reinterpret(Int64, mp_read_uint!(r, 8)))
    elseif b == 0xd9
        return String(mp_take!(r, Int(mp_read_byte!(r))))
    elseif b == 0xda
        return String(mp_take!(r, Int(mp_read_uint!(r, 2))))
    elseif b == 0xdb
        return String(mp_take!(r, Int(mp_read_uint!(r, 4))))
    elseif b == 0xdc
        return mp_decode_array!(r, Int(mp_read_uint!(r, 2)))
    elseif b == 0xdd
        return mp_decode_array!(r, Int(mp_read_uint!(r, 4)))
    elseif b == 0xde
        return mp_decode_map!(r, Int(mp_read_uint!(r, 2)))
    elseif b == 0xdf
        return mp_decode_map!(r, Int(mp_read_uint!(r, 4)))
    else
        error("msgpack: unsupported type byte")
    end
end

function mp_decode(payload::Vector{UInt8})
    return mp_decode_value!(MpReader(payload, 1))
end

# ===== framed transport over stdin/stdout ==================================

function read_frame()
    lenb = read(stdin, 4)
    length(lenb) < 4 && return nothing
    n = (UInt32(lenb[1]) << 24) | (UInt32(lenb[2]) << 16) |
        (UInt32(lenb[3]) << 8) | UInt32(lenb[4])
    payload = read(stdin, Int(n))
    length(payload) < Int(n) && return nothing
    return payload
end

function write_frame(payload::Vector{UInt8})
    n = length(payload)
    frame = UInt8[
        UInt8((n >> 24) & 0xff), UInt8((n >> 16) & 0xff),
        UInt8((n >> 8) & 0xff), UInt8(n & 0xff),
    ]
    append!(frame, payload)
    write(stdout, frame)
    flush(stdout)
end

# ===== queue server ========================================================

const HANDLERS = Dict{String,Function}()

# register exposes fn under the given command name. fn is called as
# fn(data, ctx) and its return value becomes the response; throwing sends an
# error response.
function register(name::AbstractString, fn::Function)
    HANDLERS[String(name)] = fn
end

mutable struct Context
    request_id::String
    stream::Bool
end

# cancelled always returns false in this single-task guest. It exists for API
# parity with the other runtimes.
cancelled(ctx::Context) = false

# emit publishes a partial frame on a streaming call. A dict is sent flat with
# done=false; any other value is wrapped as result. emit is a no-op when the
# call was not invoked with streaming enabled.
function emit(ctx::Context, chunk)
    (ctx.stream && !isempty(ctx.request_id)) || return
    local partial::Dict{String,Any}
    if chunk isa AbstractDict
        partial = Dict{String,Any}(string(k) => v for (k, v) in chunk)
    else
        partial = Dict{String,Any}("result" => chunk)
    end
    partial["done"] = false
    partial["request_id"] = ctx.request_id
    write_frame(mp_encode(partial))
end

function send_response(response, request_id::AbstractString)
    local payload::Dict{String,Any}
    if response isa AbstractDict
        payload = Dict{String,Any}(string(k) => v for (k, v) in response)
    else
        payload = Dict{String,Any}("result" => response)
    end
    payload["request_id"] = String(request_id)
    write_frame(mp_encode(payload))
end

describe_methods() = Dict{String,Any}(
    name => Dict{String,Any}("parameters" => Any[], "return" => Dict{String,Any}(), "doc" => "")
    for name in keys(HANDLERS)
)

function dispatch(msg::AbstractDict)
    command = get(msg, "command", "")
    command isa AbstractString || (command = "")
    data = get(msg, "data", nothing)
    request_id = get(msg, "request_id", "")
    request_id isa AbstractString || (request_id = "")
    request_id = String(request_id)
    stream = get(msg, "stream", false) === true

    if command == "exit"
        request_id != "" && send_response(Dict{String,Any}("status" => "exiting"), request_id)
        exit(0)
    elseif command == "shutdown"
        request_id != "" && send_response(Dict{String,Any}("status" => "shutting_down"), request_id)
        exit(0)
    elseif command == "__get_methods__"
        send_response(Dict{String,Any}("methods" => describe_methods()), request_id)
        return
    elseif command == "__cancel__"
        send_response(Dict{String,Any}("ok" => false, "error" => "no in-flight call"), request_id)
        return
    end

    handler = get(HANDLERS, command, nothing)
    if handler === nothing
        send_response(Dict{String,Any}("error" => "Unknown command: $command"), request_id)
        return
    end

    ctx = Context(request_id, stream)
    local result
    try
        result = handler(data, ctx)
    catch e
        if request_id != ""
            resp = Dict{String,Any}("error" => sprint(showerror, e))
            stream && (resp["done"] = true)
            send_response(resp, request_id)
        end
        return
    end
    request_id == "" && return
    if stream
        send_response(Dict{String,Any}("result" => result, "done" => true), request_id)
    else
        send_response(result, request_id)
    end
end

# serve runs the queue protocol loop over stdin/stdout until the host closes
# the connection or sends the "exit" command.
function serve()
    while true
        payload = read_frame()
        payload === nothing && return
        local msg
        try
            msg = mp_decode(payload)
        catch
            continue
        end
        msg isa AbstractDict || continue
        dispatch(msg)
    end
end
