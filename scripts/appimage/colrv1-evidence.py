#!/usr/bin/env python3
"""Loader evidence shared by the AppImage and shipped-WebKit COLRv1 checks."""
import ctypes as C
import os
from pathlib import Path
import sys


def freetype_version(library="libfreetype.so.6"):
    ft = C.CDLL(library)
    handle = C.c_void_p()
    ft.FT_Init_FreeType.argtypes = [C.POINTER(C.c_void_p)]
    ft.FT_Init_FreeType.restype = C.c_int
    if ft.FT_Init_FreeType(C.byref(handle)):
        raise RuntimeError("FT_Init_FreeType failed")
    version = [C.c_int() for _ in range(3)]
    ft.FT_Library_Version.argtypes = [C.c_void_p] + [C.POINTER(C.c_int)] * 3
    ft.FT_Library_Version(handle, *(C.byref(v) for v in version))
    ft.FT_Done_FreeType.argtypes = [C.c_void_p]
    ft.FT_Done_FreeType(handle)
    return tuple(v.value for v in version)


def process_maps(pid, appdir, label, expect_bundle=True):
    """Print real mappings, not an ldd prediction. Reject host FT in repairs."""
    proc = Path("/proc") / str(pid)
    maps = (proc / "maps").read_text()
    print(f"LOADER_PROCESS label={label} pid={pid} exe={os.readlink(proc / 'exe')}", flush=True)
    relevant = sorted({line.split(maxsplit=5)[-1] for line in maps.splitlines()
                       if any(x in line for x in ("libfreetype", "libwebkit", "libharfbuzz", "libfontconfig"))})
    for mapping in relevant:
        print(f"LOADER_MAP label={label} pid={pid} {mapping}", flush=True)
    paths = {Path(line.split(maxsplit=5)[-1]) for line in maps.splitlines()
             if "libfreetype.so" in line}
    if not paths:
        raise RuntimeError(f"no mapped FreeType in {label} pid={pid}")
    bundle = (Path(appdir) / "usr/lib/libfreetype.so.6").resolve()
    for path in paths:
        if expect_bundle and path.resolve() != bundle:
            raise RuntimeError(f"outside-image FreeType in {label}: {path}, expected {bundle}")
        if not expect_bundle and Path(appdir).resolve() in path.resolve().parents:
            raise RuntimeError(f"no-freetype mutation still uses bundle: {path}")
        print(f"LOADED_FREETYPE label={label} path={path} version={'.'.join(map(str, freetype_version(str(path))))}", flush=True)


def web_processes(parent):
    descendants = {parent}
    procs = {}
    for proc in Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        try:
            # /proc/stat's comm can contain spaces or ')'.
            stat = (proc / "stat").read_text().rsplit(") ", 1)[1].split()
            procs[int(proc.name)] = (int(stat[1]), (proc / "comm").read_text().strip())
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            continue
    while True:
        added = {pid for pid, (ppid, _) in procs.items() if ppid in descendants} - descendants
        if not added:
            break
        descendants.update(added)
    return [pid for pid, (_, comm) in procs.items()
            if pid in descendants and comm == "WebKitWebProces"]


def report_tree(parent, appdir, expect_bundle=True, label="AppImage"):
    process_maps(parent, appdir, label + "-main", expect_bundle)
    children = web_processes(parent)
    if not children:
        raise RuntimeError(f"no WebKitWebProcess below {parent}")
    for pid in children:
        process_maps(pid, appdir, label + "-WebKitWebProcess", expect_bundle)


if __name__ == "__main__":
    if sys.argv[1] == "host":
        version = freetype_version()
        print("HOST_FREETYPE version=" + ".".join(map(str, version)), flush=True)
        if version < (2, 13, 0):
            raise RuntimeError("regression host needs FreeType >=2.13")
    else:
        report_tree(int(sys.argv[1]), sys.argv[2], sys.argv[3] != "no-freetype")
