import os,sys
def o(h,m='r'):
 if sys.platform.startswith('win'):import msvcrt;return os.fdopen(msvcrt.open_osfhandle(h,os.O_RDONLY if m=='r'else os.O_WRONLY),m)
 return os.fdopen(h,m)
sys.__jbo=o;exec(o(int(sys.argv[2])).read())