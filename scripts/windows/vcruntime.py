#!/usr/bin/env python3
"""Put Microsoft's Visual C++ runtime DLLs beside the bundled PostgreSQL.

The EnterpriseDB PostgreSQL build links against the Visual C++ 2015-2022 runtime (vcruntime140.dll,
and msvcp140.dll for ICU), which a fresh Windows does not have: without it every PostgreSQL program
exits with STATUS_DLL_NOT_FOUND and the database never starts. Microsoft allows these DLLs to be
shipped beside the programs that use them, which needs no administrator rights, unlike installing
the runtime for the whole machine.

They come from Microsoft's own installer, at a fixed URL and checked by SHA-256, and each DLL is
checked too. That installer is a WiX bundle: the runtime's MSI and its cabinet sit in a cabinet
appended to the exe, which 7-Zip does not list, so it is cut out by its header and the bundle's
manifest says which payload is which. Needs 7z (p7zip-full) and msiextract (msitools); on Windows,
which has no msiextract, msiexec unpacks the MSI instead (an administrative install, which installs
nothing).

Usage: vcruntime.py DESTDIR    (writes the three DLLs into DESTDIR)
To move to a newer runtime: VCREDIST_URL from https://aka.ms/vs/17/release/vc_redist.x64.exe's
redirect, its SHA-256 (also in that URL), then the three DLL hashes this prints when they differ.
"""
import hashlib, os, re, shutil, struct, subprocess, sys, tempfile, urllib.request

VCREDIST_URL = ("https://download.visualstudio.microsoft.com/download/pr/bd1c8d9d-ba95-4eee-bc6e-df1fcc876373/"
                "CC0FF0EB1DC3F5188AE6300FAEF32BF5BEEBA4BDD6E8E445A9184072096B713B/VC_redist.x64.exe")
VCREDIST_SHA256 = "cc0ff0eb1dc3f5188ae6300faef32bf5beeba4bdd6e8e445a9184072096b713b"  # 14.44.35211
DLLS = {
    "vcruntime140.dll": "d5e4d9a3e835fa679450145d6a7d94e36573a509317111904d9b3712c30d9066",
    "vcruntime140_1.dll": "1f2d41c4aa5db0bc33ebf7b66d72943a817d7ce6cbe880502a9403823633093f",
    "msvcp140.dll": "0f885b509a685d2bbfa652fed26b5fb31d88fbdab0a978c641d1c7b8aa460aa9",
}
PACKAGE = "vcRuntimeMinimum_amd64"  # the x64 runtime; the bundle also carries an arm64 one


def sha256(b):
    return hashlib.sha256(b).hexdigest()


def cabinets(data):
    """The cabinets inside the bundle, in order: the bundle's own files, then its payloads."""
    out, i = [], 0
    while (i := data.find(b"MSCF\0\0\0\0", i)) >= 0:
        size = struct.unpack_from("<I", data, i + 8)[0]
        if 0 < size <= len(data) - i:
            out.append(data[i:i + size])
            i += size
        else:
            i += 4
    return out


def run(*args, cwd):
    subprocess.run(args, cwd=cwd, check=True, stdout=subprocess.DEVNULL)


def main(dest):
    with urllib.request.urlopen(VCREDIST_URL, timeout=300) as r:
        bundle = r.read()
    if sha256(bundle) != VCREDIST_SHA256:
        sys.exit(f"VC_redist.x64.exe has SHA-256 {sha256(bundle)}, not {VCREDIST_SHA256}")
    with tempfile.TemporaryDirectory() as tmp:
        cabs = cabinets(bundle)
        if len(cabs) < 2:
            sys.exit(f"expected the bundle's two cabinets, found {len(cabs)}")
        for n, cab in enumerate(cabs[:2]):
            open(os.path.join(tmp, f"c{n}.cab"), "wb").write(cab)
        run("7z", "x", "-y", "-oux", "c0.cab", "0", cwd=tmp)
        manifest = open(os.path.join(tmp, "ux", "0"), encoding="utf-8").read()
        ids = {}
        for p in re.finditer(r'<Payload Id="[^"]+" FilePath="([^"]+)"[^>]*?SourcePath="(a\d+)"', manifest):
            path, src = p.group(1), p.group(2)
            if path.startswith(f"packages\\{PACKAGE}\\"):
                ids[path.rsplit("\\", 1)[1]] = src
        msi = next((f for f in ids if f.endswith(".msi")), None)
        if not msi or "cab1.cab" not in ids:
            sys.exit(f"the bundle manifest has no {PACKAGE} MSI and cab1.cab: {ids}")
        run("7z", "x", "-y", "-opl", "c1.cab", ids[msi], ids["cab1.cab"], cwd=tmp)
        pkg = os.path.join(tmp, "pkg")
        os.mkdir(pkg)
        shutil.copy(os.path.join(tmp, "pl", ids[msi]), os.path.join(pkg, msi))
        shutil.copy(os.path.join(tmp, "pl", ids["cab1.cab"]), os.path.join(pkg, "cab1.cab"))
        if os.name == "nt" and not shutil.which("msiextract"):
            run("msiexec", "/a", os.path.join(pkg, msi), "/qn", "TARGETDIR=" + os.path.join(pkg, "out"), cwd=pkg)
        else:
            run("msiextract", "-C", "out", msi, cwd=pkg)
        found = {f.lower(): os.path.join(d, f) for d, _, fs in os.walk(os.path.join(pkg, "out")) for f in fs}
        wrong = []
        for name, want in DLLS.items():
            if name not in found:
                sys.exit(f"{name} is not in {msi}")
            b = open(found[name], "rb").read()
            if sha256(b) != want:
                wrong.append(f"{name}: {sha256(b)}")
                continue
            open(os.path.join(dest, name), "wb").write(b)
        if wrong:
            sys.exit("DLL checksums differ from the pinned ones:\n  " + "\n  ".join(wrong))
    for name in DLLS:
        print(f"{name} -> {dest}")


if __name__ == "__main__":
    if len(sys.argv) != 2 or not os.path.isdir(sys.argv[1]):
        sys.exit("usage: vcruntime.py DESTDIR")
    main(sys.argv[1])
