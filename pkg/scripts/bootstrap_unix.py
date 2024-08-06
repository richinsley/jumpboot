import os
import sys

# Read secondary bootstrap script from the first pipe
fd_bootstrap = int(sys.argv[2])
f_bootstrap = os.fdopen(fd_bootstrap, 'r')
secondary_script = f_bootstrap.read()
f_bootstrap.close()

# Execute the secondary bootstrap script
exec(secondary_script)