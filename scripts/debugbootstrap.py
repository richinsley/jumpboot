# jumpboot bootstrap script
print("DEBUGGING")
import debugpy
debugpy.listen(("localhost", 5678))
debugpy.wait_for_client()
breakpoint()
import os, sys
fd = sys.stderr.fileno() + 1
f = os.fdopen(fd, 'r')
script = f.read()
f.close()
exec(script)