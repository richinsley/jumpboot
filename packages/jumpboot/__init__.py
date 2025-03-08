Pipe_In = None
Pipe_Out = None

from .jsonqueue import JSONQueue, JSONQueueServer, exposed
from .namedsemaphore import NamedSemaphore

# Add msgpack functionality directly in the jumpboot package
import os

# First try to import from the installed msgpack package
try:
    # Attempt to import from the installed msgpack package
    import msgpack
    
    # Make msgpack available as jumpboot.msgpack
    msgpack = msgpack
    
except ImportError:
    # If msgpack is not installed, create our own msgpack module
    class msgpack:
        # Import the necessary components from local files
        from .msgpack.ext import ExtType, Timestamp
        from .msgpack.fallback import Packer, Unpacker, unpackb
        
        version = (1, 1, 0)
        __version__ = "1.1.0"
        
        @staticmethod
        def pack(o, stream, **kwargs):
            """
            Pack object `o` and write it to `stream`
            See :class:`Packer` for options.
            """
            packer = msgpack.Packer(**kwargs)
            stream.write(packer.pack(o))
        
        @staticmethod
        def packb(o, **kwargs):
            """
            Pack object `o` and return packed bytes
            See :class:`Packer` for options.
            """
            return msgpack.Packer(**kwargs).pack(o)
        
        @staticmethod
        def unpack(stream, **kwargs):
            """
            Unpack an object from `stream`.
            Raises `ExtraData` when `stream` contains extra bytes.
            See :class:`Unpacker` for options.
            """
            data = stream.read()
            return msgpack.unpackb(data, **kwargs)
        
        # Aliases for compatibility
        load = unpack
        loads = unpackb
        dump = pack
        dumps = packb