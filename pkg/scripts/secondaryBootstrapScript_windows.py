import os
import sys
import json
import base64
import importlib
import importlib.util
import msvcrt

def check_module_flags(module_name):
    module = importlib.import_module(module_name)
    spec = module.__spec__
    if spec:
        print(f"Module {module_name} flags:")
        print(f"  - Is frozen: {getattr(spec, '_frozen', False)}")
        print(f"  - Is built-in: {spec.origin == 'built-in'}")
        print(f"  - Origin: {spec.origin}")
        print(f"  - Has location: {spec.has_location}")
    else:
        print(f"No spec found for module {module_name}")

def load_module(name, path, source):
    module_content = base64.b64decode(source).decode('utf-8')
    spec = importlib.util.spec_from_loader(name, loader=None)
    # spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    module.__file__ = path
    module.__spec__.origin = path
    module.__spec__.has_location = True

    # Cache the source code in the module
    module.__dict__['__cached_source__'] = module_content
    exec(module_content, module.__dict__)
    sys.modules[name] = module
    # check_module_flags(name)
    return module

def load_package(package, parent_name=''):
    package_name = f"{parent_name}.{package['Name']}" if parent_name else package['Name']
    package_path = package['Path']
    package_module = importlib.util.module_from_spec(
        importlib.util.spec_from_loader(package_name, loader=None)
    )
    package_module.__path__ = [package_path]
    sys.modules[package_name] = package_module

    # Load modules in this package
    if 'Modules' in package:
        for module in package['Modules']:
            module_name = module['Name'].split('.')[0]  # Remove .py extension if present
            full_module_name = f"{package_name}.{module_name}"
            loaded_module = load_module(full_module_name, module['Path'], module['Source'])
            setattr(package_module, module_name, loaded_module)

    # Recursively load sub-packages
    if 'Packages' in package and package['Packages']:
        for sub_package in package['Packages']:
            sub_package_module = load_package(sub_package, package_name)
            sub_package_name = sub_package['Name']
            setattr(package_module, sub_package_name, sub_package_module)

    return package_module

# Read program data from the second pipe
fd_program = int(sys.argv[3])
f_program = os.fdopen(msvcrt.open_osfhandle(fd_program, os.O_RDONLY), 'r')
program_data_json = f_program.read()
f_program.close()

# create the primary pipe in/ pipe out
# the pipe python will write to is sys.argv[4]
# the pipe python will read from is sys.argv[5]
fd_out = int(sys.argv[4])
fd_in = int(sys.argv[5])
f_out = os.fdopen(msvcrt.open_osfhandle(fd_out, os.O_WRONLY), 'w')
f_in = os.fdopen(msvcrt.open_osfhandle(fd_in, os.O_RDONLY), 'r')

# attach the pipes to sys.Pipe_in and sys.Pipe_out
sys.__dict__['Pipe_in'] = f_in
sys.__dict__['Pipe_out'] = f_out

# at this point sys.argv is:
# ['-c','(int)extra_file_count', '(int)fd_bootstrap', '(int)fd_program', '(int)fd_out', '(int)fd_int', '(int)extrafile_1', 'extrafile_n', 'args'...]

# get the extra file count
extra_file_count = int(sys.argv[1])

# get the extra file descriptors, they will be 6..
extra_file_descriptors = [int(sys.argv[6 + i]) for i in range(extra_file_count - 4)]

# truncate sys.argv to just the arguments and prepend the executable name
sys.argv = ["jumpboot.py"] + sys.argv[2 + extra_file_count:]

# add the extra file descriptors to the sys module
sys.__dict__['extra_file_descriptors'] = extra_file_descriptors

# Parse the JSON data
program_data = json.loads(program_data_json)

# Load packages if present
if 'Packages' in program_data and program_data['Packages'] is not None:
    for package in program_data['Packages']:
        load_package(package)

# Load additional modules if present
if 'Modules' in program_data and program_data['Modules'] is not None:
    for module in program_data['Modules']:
        load_module(module['Name'], module['Path'], module['Source'])
    
# Load the main program
main_module = load_module(program_data['Program']['Name'],
                          program_data['Program']['Path'],
                          program_data['Program']['Source'])

# Set up and run the main module
if program_data['Program']['Name'] == '__main__':
    # Set up __main__ as it would be if this script was run directly
    sys.modules['__main__'] = main_module
    main_module.__name__ = '__main__'