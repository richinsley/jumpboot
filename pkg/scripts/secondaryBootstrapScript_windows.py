import os
import sys
import json
import base64
import importlib.util
import msvcrt

def load_module(name, path, source):
    module_content = base64.b64decode(source).decode('utf-8')
    spec = importlib.util.spec_from_loader(name, loader=None)
    module = importlib.util.module_from_spec(spec)
    module.__file__ = path
    # Cache the source code in the module
    module.__dict__['__cached_source__'] = module_content
    exec(module_content, module.__dict__)
    sys.modules[name] = module
    return module

def load_package(package):
    package_name = package['Name']
    package_path = package['Path']
    package_module = importlib.util.module_from_spec(
        importlib.util.spec_from_loader(package_name, loader=None)
    )
    package_module.__path__ = [package_path]
    sys.modules[package_name] = package_module

    for module in package['Modules']:
        full_module_name = f"{package_name}.{module['Name']}"
        loaded_module = load_module(full_module_name, module['Path'], module['Source'])
        setattr(package_module, module['Name'], loaded_module)

# Read program data from the second pipe
fd_program = int(sys.argv[3])
f_program = os.fdopen(msvcrt.open_osfhandle(fd_program, os.O_RDONLY), 'r')
program_data_json = f_program.read()
f_program.close()

# at this point sys.argv is ['-c','(int)extra_file_count', '(int)fd_bootstrap', '(int)fd_program', '(int)extrafile_1', 'extrafile_n', 'args'...]

# get the extra file count
extra_file_count = int(sys.argv[1])

# get the extra file descriptors
extra_file_descriptors = [int(sys.argv[2 + i]) for i in range(extra_file_count)]

# truncate sys.argv to just the arguments and prepend the executable name
sys.argv = ["pyingo.py"] + sys.argv[2 + extra_file_count:]

# add the extra file descriptors to the sys module
sys.__dict__['extra_file_descriptors'] = extra_file_descriptors

# Parse the JSON data
program_data = json.loads(program_data_json)

# Load packages
for package in program_data['Packages']:
    load_package(package)

# Load the main program
main_module = load_module(program_data['Program']['Name'], 
                          program_data['Program']['Path'], 
                          program_data['Program']['Source'])

# Set up and run the main module
if program_data['Program']['Name'] == '__main__':
    # Set up __main__ as it would be if this script was run directly
    sys.modules['__main__'] = main_module
    main_module.__name__ = '__main__'