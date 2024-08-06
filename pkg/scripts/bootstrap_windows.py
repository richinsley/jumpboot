import os
import sys
import msvcrt

# Read secondary bootstrap script from the first pipe
fd_bootstrap = int(sys.argv[2])
f_bootstrap = os.fdopen(msvcrt.open_osfhandle(fd_bootstrap, os.O_RDONLY), 'r')
secondary_script = f_bootstrap.read()
f_bootstrap.close()

# Execute the secondary bootstrap script
exec(secondary_script)