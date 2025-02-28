import os
import sys
import json
import base64
import importlib
import linecache
from importlib.abc import Loader, MetaPathFinder
from importlib.util import spec_from_file_location
import threading
import time
import traceback
import signal

def watchdog_monitor_parent():
    parent_pid = os.getppid()
    while True:
        try:
            if sys.platform == "win32":
                # Check if the parent PID changed, which happens when parent dies
                current_ppid = os.getppid()
                if current_ppid != parent_pid:
                    os._exit(1)
            else:
                # Unix-like systems (Linux and macOS)
                os.kill(parent_pid, 0)  # This raises OSError if process is dead
            time.sleep(3)
        except (OSError, Exception):
            # Process is dead or inaccessible
            os._exit(1)

def debug_out(msg):
    # print(f"DEBUG BOOTSTRAP: {msg}", file=sys.stderr, flush=True)
    pass

def initialize_packages(modules):
    for name, module_info in modules.items():
        if '.' not in name and module_info['Path'].endswith('__init__.py'):
            debug_out(f"Initializing package: {name}")
            importlib.import_module(name)

def print_program_structure(modules):
    def print_package(name, package, indent=""):
        debug_out(f"{indent}{name}/")
        for module in package.get('Modules', []):
            debug_out(f"{indent}  {module['Name']}")
        for sub_name, sub_package in package.get('Packages', {}).items():
            print_package(sub_name, sub_package, indent + "  ")

    debug_out("Loaded Program Structure:")
    for name, module in modules.items():
        if '.' not in name:  # Top-level modules and packages
            if isinstance(module, dict) and 'Packages' in module:
                print_package(name, module)
            else:
                debug_out(f"{name}")

class CustomFinder(MetaPathFinder):
    def __init__(self, modules):
        self.modules = modules
        self.loaded_modules = {}

    def find_spec(self, fullname, path, target=None):
        debug_out(f"Attempting to find spec for: {fullname}")
        debug_out(f"Search path: {path}")
        
        # Check if it's a module we know about
        if fullname in self.modules:
            debug_out(f"Found module: {fullname}")
            return self._create_spec(fullname)
        
        # Check if it's a name within a module we've already loaded
        parts = fullname.split('.')
        debug_out(f"Parts: {parts}")
        
        # Try to load parent modules if they haven't been loaded yet
        for i in range(1, len(parts)):
            parent_name = '.'.join(parts[:i])
            if parent_name not in self.loaded_modules and parent_name in self.modules:
                self._load_module(parent_name)
        
        # Now check for the attribute in loaded modules
        for i in range(1, len(parts)):
            parent_name = '.'.join(parts[:i])
            child_name = '.'.join(parts[i:])
            debug_out(f"Checking {child_name} in {parent_name}")
            
            if parent_name in self.loaded_modules:
                parent_module = self.loaded_modules[parent_name]
                
                # Check if the child is an attribute of the parent module
                if hasattr(parent_module, child_name):
                    debug_out(f"Found {child_name} in {parent_name}")
                    return None  # Let Python's default import mechanism handle it
        
        debug_out(f"Module not found: {fullname}")
        return None

    def _create_spec(self, fullname):
        debug_out(f"Creating spec for module: {fullname}")
        module_info = self.modules[fullname]
        source = base64.b64decode(module_info['Source']).decode('utf-8')
        loader = CustomLoader(source, module_info['Path'], fullname, self)
        spec = spec_from_file_location(fullname, module_info['Path'], loader=loader)
        
        if module_info['Path'].endswith('__init__.py'):
            spec.submodule_search_locations = [os.path.dirname(module_info['Path'])]
        
        return spec

    def _load_module(self, fullname):
        if fullname not in self.modules:
            raise ImportError(f"No module named '{fullname}'")
        
        spec = self._create_spec(fullname)
        module = importlib.util.module_from_spec(spec)
        self.loaded_modules[fullname] = module
        spec.loader.exec_module(module)
        return module

class CustomLoader(Loader):
    def __init__(self, source, path, fullname, finder):
        self.source = source
        self.fullname = fullname
        self.finder = finder
        self.path = path  # Use the module's Path directly

    def create_module(self, spec):
        return None

    def exec_module(self, module):
        debug_out(f"Executing module: {self.fullname}")
        
        # Set up module attributes
        module.__dict__['__cached_source__'] = self.source
        module.__file__ = self.path  # Set to the module's path
        module.__loader__ = self

        if self.fullname == '__main__':
            module.__package__ = ''
        elif '.' in self.fullname:
            module.__package__ = '.'.join(self.fullname.split('.')[:-1])
        else:
            module.__package__ = self.fullname

        if self.path.endswith('__init__.py'):
            module.__path__ = [os.path.dirname(self.path)]

        # Create a proper spec for the module
        spec = importlib.util.spec_from_file_location(self.fullname, self.path, loader=self)
        module.__spec__ = spec

        # Add the source to linecache with the module's path
        unique_filename = self.path
        debug_out(f"Adding source to linecache: {unique_filename}")
        linecache.cache[unique_filename] = (
            len(self.source),
            None,
            self.source.splitlines(True),
            unique_filename,
        )
        compiled = compile(self.source, unique_filename, 'exec', dont_inherit=True)

        # Execute the compiled code
        exec(compiled, module.__dict__)
        
        self.finder.loaded_modules[self.fullname] = module
        
        debug_out(f"Finished executing module: {self.fullname}")

def load_program_data(program_data):
    modules = {}
    
    def process_package(package, parent_name=''):
        package_name = f"{parent_name}.{package['Name']}" if parent_name else package['Name']
        
        # Use the Path if it ends with .py; otherwise, create a synthetic path
        if package.get('Path', '').endswith('.py'):
            package_path = package['Path']
        else:
            package_path = f"/virtual_modules/{package_name.replace('.', '/')}"
        
        # Add __init__.py for each package
        init_module = next((m for m in package.get('Modules', []) if m['Name'] == '__init__.py'), None)
        if init_module:
            init_module['Path'] = os.path.join(package_path, '__init__.py') if not init_module['Name'].endswith('.py') else init_module['Path']
            modules[package_name] = init_module
        else:
            modules[package_name] = {
                'Name': '__init__.py',
                'Path': os.path.join(package_path, '__init__.py'),
                'Source': base64.b64encode(b'').decode('utf-8')
            }
        
        if 'Modules' in package and package['Modules'] is not None:
            for module in package['Modules']:
                if module['Name'] != '__init__.py':
                    module_name = f"{package_name}.{module['Name'].split('.')[0]}"
                    if module.get('Path', '').endswith('.py'):
                        module['Path'] = module['Path']
                    else:
                        module['Path'] = os.path.join(package_path, module['Name'])
                    modules[module_name] = module

        if 'Packages' in package and package['Packages'] is not None:
            for sub_package in package['Packages']:
                process_package(sub_package, package_name)
    
    if 'Packages' in program_data and program_data['Packages'] is not None:
        for package in program_data['Packages']:
            process_package(package)

    if 'Modules' in program_data and program_data['Modules'] is not None:
        for module in program_data['Modules']:
            # Use Path if it ends with .py
            if module.get('Path', '').endswith('.py'):
                module['Path'] = module['Path']
            else:
                module['Path'] = f"/virtual_modules/{module['Name']}"
            modules[module['Name']] = module

    # Ensure the main program has the Path
    if program_data['Program'].get('Path', '').endswith('.py'):
        program_data['Program']['Path'] = program_data['Program']['Path']
    else:
        program_data['Program']['Path'] = f"/virtual_modules/{program_data['Program']['Name']}"
    modules[program_data['Program']['Name']] = program_data['Program']

    return modules


# Read program data from the second pipe
fd_program = int(sys.argv[3])
with sys.__jbo(fd_program, 'r') as f_program:
    program_data = json.loads(f_program.read())

# Set up pipes
fd_out = program_data['PipeOut']
fd_in = program_data['PipeIn']
fd_status = program_data['StatusIn']
f_out = sys.__jbo(fd_out, 'w')
f_in = sys.__jbo(fd_in, 'r')
f_status = sys.__jbo(fd_status, 'w')

# Process extra file descriptors
extra_file_count = int(sys.argv[1])
extra_file_descriptors = [int(sys.argv[4 + i]) for i in range(extra_file_count - 2)]
sys.extra_file_descriptors = extra_file_descriptors

# show the length of  extra file descriptors are passed
debug_out(f"Extra file descriptors: {len(sys.extra_file_descriptors)}")

# Adjust sys.argv
sys.argv = ["pyingo.py"] + sys.argv[2 + extra_file_count:]

# Process program data
modules = load_program_data(program_data)

# Create an instance of CustomFinder
custom_finder = CustomFinder(modules)

# Add the custom finder to sys.meta_path
sys.meta_path.insert(0, custom_finder)

# Handle main program
main_module_name = program_data['Program']['Name']
main_module_info = modules[main_module_name]
main_source = base64.b64decode(main_module_info['Source']).decode('utf-8')

# Load all top-level packages
if modules is not None:
    for name in modules:
        if '.' not in name and name != main_module_name:
            custom_finder._load_module(name)

# get the "jumpboot" package
jumpboot_package = importlib.import_module('jumpboot')

if jumpboot_package is not None:
    # Attach pipes to jumpboot.Pipe_in, jumpboot.Pipe_out, and jumpboot.Status_in
    setattr(jumpboot_package, "Pipe_in", f_in)
    setattr(jumpboot_package, "Pipe_out", f_out)
    setattr(jumpboot_package, "Status_in", f_status)

    # process the the KVPairs.  Assign each key value pair to jumpboot package so that it is available as jumpboot.key
    if 'KVPairs' in program_data and program_data['KVPairs'] is not None:
        for key, value in program_data['KVPairs'].items():
            setattr(jumpboot_package, key, value)

# Now load and execute the main module
main_module_info = modules[main_module_name]
main_source = base64.b64decode(main_module_info['Source']).decode('utf-8')

loader = CustomLoader(main_source, main_module_info['Path'], '__main__', custom_finder)
spec = importlib.util.spec_from_file_location('__main__', main_module_info['Path'], loader=loader)
main_module = importlib.util.module_from_spec(spec)
sys.modules['__main__'] = main_module

# if program_data has a field 'DebugPort' and it is not None and non-zero, then start the debug server
# and wait for the client to connect and stop at the breakpoint.
if 'DebugPort' in program_data and program_data['DebugPort'] is not None and program_data['DebugPort'] != 0:
    # check if debugpy is installed and if not, install it
    try:
        import debugpy
    except ImportError:
        import subprocess
        subprocess.run([sys.executable, '-m', 'pip', 'install', 'debugpy'])
        import debugpy
    print("DEBUGGING")
    debugpy.listen(("localhost", program_data['DebugPort']))
    debugpy.wait_for_client()
    if 'BreakOnStart' in program_data and program_data['BreakOnStart']:
        breakpoint()

# watchdog to monitor for the parent process - if the parent process dies, then exit the child process
# Start the monitor thread as daemon (so it won't prevent program exit)
monitor_thread = threading.Thread(target=watchdog_monitor_parent, daemon=True)
monitor_thread.start()

# loader.exec_module(main_module)

try:
    loader.exec_module(main_module)
except Exception as e:
    # Signal exception
    exception_info = {
        "type": "exception",
        "exception": type(e).__name__,
        "message": str(e),
        "traceback": traceback.format_exc(),
    }
    print("Exception:", exception_info, file=sys.stderr)
    f_status.write(json.dumps(exception_info) + "\n")
    f_status.flush()
finally:
    # Signal completion
    exception_info = {
        "type": "status",
        "message": "exit",
    }
    print("Exiting")
    f_status.write(json.dumps(exception_info) + "\n")
    f_status.flush()